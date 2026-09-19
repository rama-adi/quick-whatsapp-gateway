package main

import (
	"context"
	"errors"
	"testing"
	"time"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/journal"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestControlEventSinkFencesAndNormalizesEvent(t *testing.T) {
	ctx := context.Background()
	j, err := journal.Open(ctx, t.TempDir()+"/events.db", journal.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	sink := controlEventSink{
		adapter:    &journal.ControlAdapter{Journal: j, GatewayID: "gw_1"},
		assignment: func(org, session string) (uint64, bool) { return 7, org == "org_1" && session == "sess_1" },
	}
	event := domain.Event{Schema: domain.Schema, ID: "evt_1", Type: domain.EventMessage, Session: "sess_1", Organization: "org_1", Timestamp: time.Now().UnixMilli(), Payload: map[string]any{"body": "hi"}}
	if err := sink.Publish(ctx, event); err != nil {
		t.Fatal(err)
	}
	sink.adapter.SetDesiredAssignments(&gatewayv1.DesiredStateSnapshot{Assignments: []*gatewayv1.SessionAssignment{{SessionId: "sess_1", OrganizationId: "org_1", AssignmentEpoch: 7}}})
	batch, err := sink.adapter.NextEventBatch(ctx, 11)
	if err != nil || len(batch.Events) != 1 {
		t.Fatalf("batch = %#v, %v", batch, err)
	}
	got := batch.Events[0]
	if got.GetGatewayId() != "gw_1" || got.GetConnectionEpoch() != 11 || got.GetAssignmentEpoch() != 7 || got.GetOrganizationId() != "org_1" || got.GetSessionId() != "sess_1" || got.GetEventType() != domain.EventMessage {
		t.Fatalf("unexpected control event: %#v", got)
	}
	var payload structpb.Struct
	if err := proto.Unmarshal(got.GetPayload(), &payload); err != nil || payload.GetFields()["id"].GetStringValue() != "evt_1" {
		t.Fatalf("payload id = %q, %v", payload.GetFields()["id"].GetStringValue(), err)
	}
	if err := sink.Publish(ctx, domain.Event{ID: "evt_unassigned", Session: "other", Organization: "org_1", Timestamp: time.Now().UnixMilli()}); err == nil {
		t.Fatal("unassigned event accepted")
	}
	if _, err := sink.adapter.Batch(ctx, 1, 1, "gw_1", 11, func([]byte) (*gatewayv1.GatewayEvent, error) {
		return &gatewayv1.GatewayEvent{}, nil
	}); !errors.Is(err, journal.ErrControlEventTooLarge) {
		t.Fatalf("oversize batch error = %v", err)
	}
}
