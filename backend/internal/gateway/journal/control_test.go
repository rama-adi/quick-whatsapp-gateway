package journal

import (
	"context"
	"encoding/json"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"strings"
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

// An entry that can never decode is quarantined (kept for repair) and replay
// continues with the next entry instead of failing every batch.
func TestReplayQuarantinesMalformedJournalEntryAndContinues(t *testing.T) {
	ctx := context.Background()
	j, _ := openTestJournal(t, DefaultConfig())
	if _, _, err := j.Append(ctx, "malformed", []byte("invalid JSON"), time.Now()); err != nil {
		t.Fatal(err)
	}
	adapter := &ControlAdapter{Journal: j, GatewayID: "gateway"}
	event := domain.NewEvent(domain.EventMessage, "session", "org", map[string]any{})
	good, _, err := adapter.AppendDomainEvent(ctx, event, 1)
	if err != nil {
		t.Fatal(err)
	}
	adapter.SetDesiredAssignments(&gatewayv1.DesiredStateSnapshot{Revision: 1, Assignments: []*gatewayv1.SessionAssignment{
		{SessionId: "session", OrganizationId: "org", AssignmentEpoch: 1},
	}})
	batch, err := adapter.NextEventBatch(ctx, 1)
	if err != nil || len(batch.Events) != 1 || batch.Events[0].JournalSequence != good.Seq {
		t.Fatalf("malformed entry blocked replay: %+v %v", batch, err)
	}
	if n, err := j.QuarantinedEntries(ctx); err != nil || n != 1 {
		t.Fatalf("malformed entry not quarantined: %d %v", n, err)
	}
	var payload []byte
	if err := j.db.QueryRowContext(ctx, `SELECT payload FROM journal_quarantine WHERE event_id='malformed'`).Scan(&payload); err != nil || string(payload) != "invalid JSON" {
		t.Fatalf("quarantined bytes lost: %q %v", payload, err)
	}
}

func TestReplayQuarantinesEntriesMissingOwnershipMetadata(t *testing.T) {
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
			if batch, err := adapter.NextEventBatch(ctx, 1); err != nil || len(batch.Events) != 0 {
				t.Fatalf("invalid ownership entry: %+v %v", batch, err)
			}
			metrics, err := j.Metrics(ctx)
			if err != nil || metrics.Entries != 0 {
				t.Fatalf("entry still blocks replay: %+v %v", metrics, err)
			}
			if n, err := j.QuarantinedEntries(ctx); err != nil || n != 1 {
				t.Fatalf("entry not kept in quarantine: %d %v", n, err)
			}
		})
	}
}

// An entry larger than any control batch is quarantined rather than failing
// every replay attempt.
func TestReplayQuarantinesOversizedEntry(t *testing.T) {
	ctx := context.Background()
	j, _ := openTestJournal(t, DefaultConfig())
	adapter := &ControlAdapter{Journal: j, GatewayID: "gateway"}
	big := domain.NewEvent(domain.EventMessage, "session", "org", map[string]any{"text": strings.Repeat("x", 4096)})
	if _, _, err := adapter.AppendDomainEvent(ctx, big, 1); err != nil {
		t.Fatal(err)
	}
	small := domain.NewEvent(domain.EventMessage, "session", "org", map[string]any{})
	next, _, err := adapter.AppendDomainEvent(ctx, small, 1)
	if err != nil {
		t.Fatal(err)
	}
	assignments := map[string]eventAssignment{"session": {organization: "org", epoch: 1}}
	if _, err := adapter.batch(ctx, 10, 1024, "gateway", 1, decodeJournalEvent, assignments); err != nil {
		t.Fatalf("oversized entry failed replay: %v", err)
	}
	batch, err := adapter.batch(ctx, 10, 1024, "gateway", 1, decodeJournalEvent, assignments)
	if err != nil || len(batch.Events) != 1 || batch.Events[0].JournalSequence != next.Seq {
		t.Fatalf("oversized entry blocked replay: %+v %v", batch, err)
	}
	if n, err := j.QuarantinedEntries(ctx); err != nil || n != 1 {
		t.Fatalf("oversized entry not quarantined: %d %v", n, err)
	}
}

func TestMediaDescriptorStaysPrivateAcrossDurableHandoff(t *testing.T) {
	ctx := context.Background()
	j, _ := openTestJournal(t, DefaultConfig())
	adapter := &ControlAdapter{Journal: j, GatewayID: "gateway"}
	event := domain.NewEvent(domain.EventMessage, "session", "org", map[string]any{"waMessageId": "message"})
	event.MediaSource = "private-source"
	public, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), "private-source") {
		t.Fatal("public envelope exposed descriptor")
	}
	entry, _, err := adapter.AppendDomainEvent(ctx, event, 1)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := decodeJournalEvent(entry.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var envelope structpb.Struct
	if err = proto.Unmarshal(wire.Payload, &envelope); err != nil {
		t.Fatal(err)
	}
	payload := envelope.AsMap()["payload"].(map[string]any)
	if payload["_mediaSource"] != "private-source" {
		t.Fatal("durable handoff lost descriptor")
	}
}
