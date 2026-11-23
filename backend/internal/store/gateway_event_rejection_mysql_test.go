package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
)

// A rejected journal entry is recorded and leaves no partial state (dedupe
// row, outgoing-echo claim, event log, fan-out), while the rest of its batch
// is accepted in order. The batch commits, so the control stream acknowledges
// it and the gateway journal advances past the rejected entry.
func TestGatewayEventIngestMySQLRejectsOneEntryAndAcceptsTheRest(t *testing.T) {
	db := lifecycleTestDB(t)
	template, session := fanoutFixture(t, db)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM gateway_event_rejections WHERE session_id=?`, session.ID) })
	repo := NewGatewayEventIngestRepo(db)
	repo.MediaCapture = func(_ context.Context, _ storedb.DBTX, e GatewayEvent) ([]byte, error) {
		if e.EventID == template.EventID+"_badmedia" {
			return nil, fmt.Errorf("%w: undecodable media descriptor", ErrGatewayEventInvalid)
		}
		return e.Payload, nil
	}
	mk := func(suffix string, seq uint64) GatewayEvent {
		e := template
		e.EventID += suffix
		e.JournalSequence = seq
		return e
	}
	first, undecodable, badMedia, last := mk("_first", 1), mk("_undecodable", 2), mk("_badmedia", 3), mk("_last", 4)
	undecodable.RejectReason = "decode gateway event payload: bad envelope"
	badMedia.Type = domain.EventMessageFromMe
	badMedia.Payload = []byte(`{"fromMe":true,"waMessageId":"wam_bad","chatJid":"1@s.whatsapp.net"}`)

	rejected, err := repo.IngestBatch(ctx, []GatewayEvent{first, undecodable, badMedia, last}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 2 || rejected[0].EventID != undecodable.EventID || rejected[1].EventID != badMedia.EventID ||
		rejected[0].JournalSequence != 2 || rejected[1].JournalSequence != 3 {
		t.Fatalf("rejections = %+v", rejected)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM gateway_event_rejections WHERE session_id=?`, session.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("recorded rejections=%d err=%v", count, err)
	}
	for _, id := range []string{undecodable.EventID, badMedia.EventID} {
		for _, q := range []string{
			`SELECT COUNT(*) FROM gateway_ingested_events WHERE gateway_event_id=?`,
			`SELECT COUNT(*) FROM event_log WHERE event_id=?`,
			`SELECT COUNT(*) FROM event_fanout WHERE event_id=?`,
			`SELECT COUNT(*) FROM outgoing_message_event_claims WHERE event_id=?`,
		} {
			if err := db.QueryRow(q, id).Scan(&count); err != nil || count != 0 {
				t.Fatalf("%s left state (%s): %d err=%v", id, q, count, err)
			}
		}
	}
	claimed := fanoutClaim(t, NewEventFanoutRepo(db), domain.NewULID(), 1000, 10)
	if got := fanoutIDs(claimed, session.ID); len(got) != 2 || got[0] != first.EventID || got[1] != last.EventID ||
		claimed[0].SessionSeq+1 != claimed[1].SessionSeq {
		t.Fatalf("accepted events = %v", got)
	}
	// Replaying the same batch (acknowledgement lost) is idempotent.
	if rejected, err = repo.IngestBatch(ctx, []GatewayEvent{first, undecodable, badMedia, last}, 200); err != nil || len(rejected) != 2 {
		t.Fatalf("replay rejected=%d err=%v", len(rejected), err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM gateway_event_rejections WHERE session_id=?`, session.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("replayed rejections=%d err=%v", count, err)
	}
}
