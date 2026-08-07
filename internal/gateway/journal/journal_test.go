package journal

import (
	"context"
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

func TestAppendDeduplicatesAndReplaysInSequence(t *testing.T) {
	ctx := context.Background()
	j, path := openTestJournal(t, testConfig(1024))
	first, inserted, err := j.Append(ctx, "evt_1", []byte("one"), time.UnixMilli(100))
	if err != nil || !inserted || first.Seq == 0 {
		t.Fatalf("first append = %+v inserted=%v err=%v", first, inserted, err)
	}
	duplicate, inserted, err := j.Append(ctx, "evt_1", []byte("changed"), time.UnixMilli(200))
	if err != nil || inserted || duplicate.Seq != first.Seq || string(duplicate.Payload) != "one" {
		t.Fatalf("duplicate append = %+v inserted=%v err=%v", duplicate, inserted, err)
	}
	second, inserted, err := j.Append(ctx, "evt_2", []byte("two"), time.UnixMilli(300))
	if err != nil || !inserted || second.Seq <= first.Seq {
		t.Fatalf("second append = %+v inserted=%v err=%v", second, inserted, err)
	}
	entries, err := j.ReadUnacked(ctx, 10, 1024)
	if err != nil || len(entries) != 2 || entries[0].EventID != "evt_1" || entries[1].EventID != "evt_2" {
		t.Fatalf("entries = %+v err=%v", entries, err)
	}
	if err := j.Ack(ctx, first.Seq); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path, testConfig(1024))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	entries, err = reopened.ReadUnacked(ctx, 10, 1024)
	if err != nil || len(entries) != 1 || entries[0].EventID != "evt_2" || entries[0].Seq != second.Seq {
		t.Fatalf("replayed entries = %+v err=%v", entries, err)
	}
	metrics, err := reopened.Metrics(ctx)
	if err != nil || metrics.AckedThrough != first.Seq || metrics.Entries != 1 || metrics.OldestUnackedAt == nil {
		t.Fatalf("metrics = %+v err=%v", metrics, err)
	}
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
