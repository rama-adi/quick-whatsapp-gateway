package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testConfig(maxBytes int64) Config {
	return Config{MaxBytes: maxBytes, VolumeBytes: maxBytes * 4}
}

func openTestJournal(t *testing.T, cfg Config) (*Journal, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(context.Background(), path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j, path
}

func TestAckWatermarkIsMonotonicAndRejectsFutureSequence(t *testing.T) {
	ctx := context.Background()
	j, _ := openTestJournal(t, testConfig(1024))
	entry, _, err := j.Append(ctx, "evt_1", []byte("one"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Ack(ctx, entry.Seq); err != nil {
		t.Fatal(err)
	}
	if err := j.Ack(ctx, entry.Seq); err != nil {
		t.Fatalf("duplicate ack: %v", err)
	}
	if err := j.Ack(ctx, entry.Seq+1); !errors.Is(err, ErrUnknownAck) {
		t.Fatalf("future ack error = %v", err)
	}
}

func TestCapacityMetricsAndBackpressure(t *testing.T) {
	ctx := context.Background()
	j, _ := openTestJournal(t, testConfig(100))
	appendBytes := func(id string, n int) {
		t.Helper()
		if _, _, err := j.Append(ctx, id, make([]byte, n), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	appendBytes("evt_70", 70)
	metrics, err := j.Metrics(ctx)
	if err != nil || metrics.State != CapacityDegraded || metrics.Bytes != 70 || metrics.Entries != 1 {
		t.Fatalf("70%% metrics = %+v err=%v", metrics, err)
	}
	appendBytes("evt_80", 10)
	metrics, _ = j.Metrics(ctx)
	if metrics.State != CapacityPaused {
		t.Fatalf("80%% state = %q", metrics.State)
	}
	appendBytes("evt_90", 10)
	metrics, _ = j.Metrics(ctx)
	if metrics.State != CapacityCritical {
		t.Fatalf("90%% state = %q", metrics.State)
	}
	if _, _, err = j.Append(ctx, "evt_full", make([]byte, 11), time.Now()); !errors.Is(err, ErrCapacity) {
		t.Fatalf("full journal error = %v", err)
	}
}

func TestConfigRejectsCapOverQuarterOfVolume(t *testing.T) {
	_, err := Open(context.Background(), filepath.Join(t.TempDir(), "journal.db"), Config{MaxBytes: 26, VolumeBytes: 100})
	if err == nil {
		t.Fatal("over-quarter journal cap accepted")
	}
}

func TestOpenRejectsCorruptExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), path, testConfig(1024))
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("error = %v, want corrupt", err)
	}
}

func TestConcurrentAppendKeepsAllUniqueEvents(t *testing.T) {
	ctx := context.Background()
	j, _ := openTestJournal(t, testConfig(1<<20))
	const writers = 32
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, inserted, err := j.Append(ctx, fmt.Sprintf("evt_%d", i), []byte("event"), time.Now())
			if err != nil || !inserted {
				errs <- fmt.Errorf("append %d inserted=%v: %w", i, inserted, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	metrics, err := j.Metrics(ctx)
	if err != nil || metrics.Entries != writers || metrics.Bytes != writers*int64(len("event")) {
		t.Fatalf("metrics = %+v err=%v", metrics, err)
	}
}

func TestCommittedEntriesSurviveAbruptDatabaseClose(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(ctx, path, testConfig(1024))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = j.Append(ctx, "evt_1", []byte("one"), time.Now()); err != nil {
		t.Fatal(err)
	}
	// Deliberately skip Journal.Close: a committed WAL transaction must replay
	// after a process crash before its checkpoint hook can run.
	if err = j.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path, testConfig(1024))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	entries, err := reopened.ReadUnacked(ctx, 10, 1024)
	if err != nil || len(entries) != 1 || entries[0].EventID != "evt_1" {
		t.Fatalf("entries after abrupt close = %+v err=%v", entries, err)
	}
}

// A journal created before give_up_at existed gains the column, and its
// stored notifications get the default 24h deadline from creation.
func TestOpenMigratesHistoryInboxGiveUpDeadline(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE history_inbox (
id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, organization_id TEXT NOT NULL,
notification_id TEXT NOT NULL, account_jid TEXT NOT NULL, notification BLOB NOT NULL,
notified_at INTEGER NOT NULL, cutoff_at INTEGER NOT NULL, import_enabled INTEGER NOT NULL,
status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at INTEGER NOT NULL DEFAULT 0,
batches_done INTEGER NOT NULL DEFAULT 0, server_error_sent INTEGER NOT NULL DEFAULT 0,
error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
UNIQUE(session_id, notification_id));
INSERT INTO history_inbox (session_id, organization_id, notification_id, account_jid, notification,
notified_at, cutoff_at, import_enabled, status, created_at, updated_at)
VALUES ('s','o','n','a',x'01',1,0,1,'pending',1000,1000);`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	j, err := Open(ctx, path, Config{MaxBytes: 1 << 20, VolumeBytes: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	rows, err := j.PendingHistory(ctx)
	if err != nil || len(rows) != 1 || rows[0].GiveUpAt.UnixMilli() != 1000+(24*time.Hour).Milliseconds() {
		t.Fatalf("rows = %+v err=%v", rows, err)
	}
}
