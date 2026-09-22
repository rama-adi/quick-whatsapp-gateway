package journal

import (
	"context"
	"testing"
	"time"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func TestReplayRetiresRemovedAssignmentsWithoutBlockingOwnedEvents(t *testing.T) {
	ctx := context.Background()
	j, _ := openTestJournal(t, DefaultConfig())
	adapter := &ControlAdapter{Journal: j, GatewayID: "gateway"}
	appendEvent := func(id, session string, epoch uint64) Entry {
		t.Helper()
		event := domain.NewEvent(domain.EventMessage, session, "org", map[string]any{"text": id})
		event.ID = id
		entry, _, err := adapter.AppendDomainEvent(ctx, event, epoch)
		if err != nil {
			t.Fatal(err)
		}
		return entry
	}
	appendEvent("deleted-session", "deleted", 1)
	appendEvent("old-epoch", "active", 1)
	retained := appendEvent("retained", "active", 2)
	tail := appendEvent("deleted-tail", "deleted", 1)
	snapshot := &gatewayv1.DesiredStateSnapshot{Revision: 2, Assignments: []*gatewayv1.SessionAssignment{
		{SessionId: "active", OrganizationId: "org", AssignmentEpoch: 2},
	}}
	if batch, err := adapter.NextEventBatch(ctx, 1); err != nil || len(batch.Events) != 0 {
		t.Fatalf("replayed without snapshot: %+v %v", batch, err)
	}
	adapter.SetDesiredAssignments(snapshot)
	// An out-of-order older empty snapshot must not retire the active session.
	adapter.SetDesiredAssignments(&gatewayv1.DesiredStateSnapshot{Revision: 1})
	batch, err := adapter.NextEventBatch(ctx, 1)
	if err != nil || len(batch.Events) != 1 || batch.Events[0].JournalSequence != retained.Seq {
		t.Fatalf("owned batch: %+v %v", batch, err)
	}
	// Transport failure/reconnect does not acknowledge any event or carry over
	// prior connection authority. Same-epoch ownership then permits replay.
	adapter.ResetDesiredAssignments()
	if batch, err := adapter.NextEventBatch(ctx, 2); err != nil || len(batch.Events) != 0 {
		t.Fatalf("replayed before reconnect snapshot: %+v %v", batch, err)
	}
	adapter.SetDesiredAssignments(snapshot)
	batch, err = adapter.NextEventBatch(ctx, 2)
	if err != nil || len(batch.Events) != 1 || batch.Events[0].ConnectionEpoch != 2 {
		t.Fatalf("reconnected batch: %+v %v", batch, err)
	}
	if err := adapter.AckEvents(ctx, retained.Seq); err != nil {
		t.Fatal(err)
	}
	batch, err = adapter.NextEventBatch(ctx, 2)
	if err != nil || len(batch.Events) != 0 {
		t.Fatalf("retired tail: %+v %v", batch, err)
	}
	metrics, err := j.Metrics(ctx)
	if err != nil || metrics.Entries != 0 || metrics.AckedThrough != tail.Seq {
		t.Fatalf("metrics after retirement: %+v %v", metrics, err)
	}
	next := appendEvent("next-active", "active", 2)
	batch, err = adapter.NextEventBatch(ctx, 2)
	if err != nil || len(batch.Events) != 1 || batch.Events[0].JournalSequence != next.Seq {
		t.Fatalf("later active event blocked: %+v %v", batch, err)
	}
}

func TestReplayPreservesMalformedJournalEntryForVisibleRecovery(t *testing.T) {
	ctx := context.Background()
	j, _ := openTestJournal(t, DefaultConfig())
	if _, _, err := j.Append(ctx, "malformed", []byte("invalid JSON"), time.Now()); err != nil {
		t.Fatal(err)
	}
	adapter := &ControlAdapter{Journal: j, GatewayID: "gateway"}
	adapter.SetDesiredAssignments(&gatewayv1.DesiredStateSnapshot{Revision: 1})
	if _, err := adapter.NextEventBatch(ctx, 1); err == nil {
		t.Fatal("malformed entry silently discarded")
	}
	metrics, err := j.Metrics(ctx)
	if err != nil || metrics.Entries != 1 {
		t.Fatalf("malformed entry lost: %+v %v", metrics, err)
	}
}

func TestReplayPreservesEntriesMissingOwnershipMetadata(t *testing.T) {
	for _, field := range []string{"session", "organization"} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			j, _ := openTestJournal(t, DefaultConfig())
			adapter := &ControlAdapter{Journal: j, GatewayID: "gateway"}
			event := domain.NewEvent(domain.EventMessage, "session", "org", map[string]any{})
			if field == "session" {
				event.Session = ""
			} else {
				event.Organization = ""
			}
			if _, _, err := adapter.AppendDomainEvent(ctx, event, 1); err != nil {
				t.Fatal(err)
			}
			adapter.SetDesiredAssignments(&gatewayv1.DesiredStateSnapshot{Revision: 1})
			if _, err := adapter.NextEventBatch(ctx, 1); err == nil {
				t.Fatal("invalid ownership treated as retired assignment")
			}
			metrics, err := j.Metrics(ctx)
			if err != nil || metrics.Entries != 1 || metrics.AckedThrough != 0 {
				t.Fatalf("malformed ownership entry lost: %+v %v", metrics, err)
			}
		})
	}
}
