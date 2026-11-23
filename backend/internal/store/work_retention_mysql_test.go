package store

import (
	"context"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// Work retention removes finished work and dedupe ledgers past their
// windows, keeps recent rows, and never deletes unfinished fan-out.
func TestRetentionMySQLPruneWork(t *testing.T) {
	db := lifecycleTestDB(t)
	template, session := fanoutFixture(t, db)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM gateway_event_rejections WHERE session_id=?`, session.ID)
		_, _ = db.Exec(`DELETE FROM history_sync_batches WHERE session_id=?`, session.ID)
	})
	now := time.Now().UnixMilli()
	old := now - (WorkLedgerRetention + time.Hour).Milliseconds()
	ingest := NewGatewayEventIngestRepo(db)
	oldDone, oldPending, recent := template, template, template
	oldDone.EventID += "_old_done"
	oldPending.EventID += "_old_pending"
	recent.EventID += "_recent"
	recent.Type = domain.EventMessageFromMe
	recent.Payload = []byte(`{"fromMe":true,"waMessageId":"wam_recent","chatJid":"1@s.whatsapp.net"}`)
	for _, e := range []struct {
		ev GatewayEvent
		at int64
	}{{oldDone, old}, {oldPending, old}, {recent, now}} {
		if err := ingest.Ingest(ctx, e.ev, e.at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE event_fanout SET status='done', finished_at=? WHERE event_id=?`, old, oldDone.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE outgoing_message_event_claims SET created_at=? WHERE session_id=?`, old, session.ID); err != nil {
		t.Fatal(err)
	}
	for id, at := range map[string]int64{"hsb_old_" + session.ID: old, "hsb_new_" + session.ID: now} {
		if _, err := db.Exec(`INSERT INTO history_sync_batches (event_id, session_id, run_id, applied_at)
			VALUES (?, ?, 'r', ?)`, id, session.ID, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO gateway_event_rejections (gateway_event_id, gateway_id, connection_epoch,
		journal_sequence, session_id, organization_id, event_type, reason, payload_bytes, rejected_at)
		VALUES (?, ?, 1, 1, ?, 'org', 'message', 'bad', 1, ?)`, "rej_"+session.ID, template.GatewayID, session.ID, old); err != nil {
		t.Fatal(err)
	}
	res, err := NewRetentionRepo(db).PruneWork(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.FanoutDone < 1 || res.IngestedEvents < 2 || res.OutgoingClaims < 1 || res.Rejections < 1 || res.HistoryBatches < 1 {
		t.Fatalf("pruned = %+v", res)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM event_fanout WHERE event_id=?`, oldPending.EventID); n != 1 {
		t.Fatal("unfinished fan-out was pruned")
	}
	if n := count(`SELECT COUNT(*) FROM gateway_ingested_events WHERE gateway_event_id=?`, recent.EventID); n != 1 {
		t.Fatal("recent dedupe row was pruned")
	}
	if n := count(`SELECT COUNT(*) FROM history_sync_batches WHERE session_id=?`, session.ID); n != 1 {
		t.Fatalf("history ledger rows left = %d, want only the recent one", n)
	}
	// A replay of a pruned dedupe row is still a no-op: the event is logged.
	if err := ingest.Ingest(ctx, oldDone, now); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT COUNT(*) FROM event_fanout WHERE event_id=?`, oldDone.EventID); n != 0 {
		t.Fatal("replay after dedupe pruning enqueued fan-out again")
	}
}
