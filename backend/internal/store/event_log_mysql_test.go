package store

import (
	"context"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// TestEventLogReplaySkipsBulkEvents pins the replay SQL to the bulk-event rule:
// history.batch rows stay in event_log (webhook deliveries read their payloads)
// but ?since replay for both org and session scope skips them.
func TestEventLogReplaySkipsBulkEvents(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	org := "org_" + domain.NewULID()
	session := domain.NewSessionID()
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM event_log WHERE organization_id=?`, org); err != nil {
			t.Error(err)
		}
	})
	repo := NewEventLogRepo(db)
	var batchID string
	for _, typ := range []string{domain.EventHistorySync, domain.EventHistoryBatch, domain.EventMessage} {
		e := domain.EventLogEntry{
			EventID: domain.NewEventID(), OrganizationID: org, SessionID: session,
			Type: typ, Payload: []byte(`{}`), CreatedAt: 1,
		}
		if typ == domain.EventHistoryBatch {
			batchID = e.EventID
		}
		if _, err := repo.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []string{"", session} {
		got, err := repo.ListSince(ctx, org, scope, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Type != domain.EventHistorySync || got[1].Type != domain.EventMessage {
			t.Fatalf("scope %q replayed %+v", scope, got)
		}
	}
	if _, err := repo.GetByEventID(ctx, batchID); err != nil {
		t.Fatalf("bulk event must stay addressable for webhook delivery: %v", err)
	}
}
