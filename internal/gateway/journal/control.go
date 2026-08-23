package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

var ErrControlEventTooLarge = errors.New("gateway journal event exceeds control batch capacity")

// ControlAdapter maps the durable local journal to the private control-stream
// contract. The journal sequence is the only acknowledgement cursor.
type ControlAdapter struct {
	Journal   *Journal
	GatewayID string
}

func (a ControlAdapter) Batch(
	ctx context.Context,
	maxEntries int,
	maxBytes int64,
	gatewayID string,
	connectionEpoch uint64,
	metadata func([]byte) (*gatewayv1.GatewayEvent, error),
) (*gatewayv1.GatewayEventBatch, error) {
	entries, err := a.Journal.ReadUnacked(ctx, maxEntries, maxBytes)
	if err != nil {
		return nil, err
	}
	batch := &gatewayv1.GatewayEventBatch{Events: make([]*gatewayv1.GatewayEvent, 0, len(entries))}
	for _, entry := range entries {
		event, decodeErr := metadata(entry.Payload)
		if decodeErr != nil {
			return nil, decodeErr
		}
		event.JournalSequence = entry.Seq
		event.EventId = entry.EventID
		event.GatewayId = gatewayID
		event.ConnectionEpoch = connectionEpoch
		batch.Events = append(batch.Events, event)
		if int64(proto.Size(batch)) > maxBytes {
			batch.Events = batch.Events[:len(batch.Events)-1]
			if len(batch.Events) == 0 {
				return nil, ErrControlEventTooLarge
			}
			break
		}
	}
	return batch, nil
}

func (a ControlAdapter) Ack(ctx context.Context, sequence uint64) error {
	return a.Journal.Ack(ctx, sequence)
}

func (a ControlAdapter) AckEvents(ctx context.Context, sequence uint64) error {
	return a.Ack(ctx, sequence)
}

func (a ControlAdapter) NextEventBatch(ctx context.Context, epoch uint64) (*gatewayv1.GatewayEventBatch, error) {
	return a.Batch(ctx, DefaultBatchEntries, DefaultBatchBytes, a.GatewayID, epoch, decodeJournalEvent)
}

// decodeJournalEvent converts one stored journal payload into the private
// control-stream event representation.
func decodeJournalEvent(payload []byte) (*gatewayv1.GatewayEvent, error) {
	var persisted persistedEvent
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, fmt.Errorf("decode journal event: %w", err)
	}
	missingMetadata := persisted.Event.ID == "" || persisted.AssignmentEpoch == 0
	if missingMetadata {
		return nil, errors.New("journal event is missing canonical assignment metadata")
	}
	jsonPayload, err := json.Marshal(persisted.Event)
	if err != nil {
		return nil, fmt.Errorf("marshal normalized event: %w", err)
	}
	var value map[string]any
	if err := json.Unmarshal(jsonPayload, &value); err != nil {
		return nil, fmt.Errorf("decode normalized event JSON: %w", err)
	}
	payloadStruct, err := structpb.NewStruct(value)
	if err != nil {
		return nil, fmt.Errorf("build normalized event payload: %w", err)
	}
	encoded, err := proto.Marshal(payloadStruct)
	if err != nil {
		return nil, fmt.Errorf("marshal normalized event payload: %w", err)
	}
	return &gatewayv1.GatewayEvent{
		AssignmentEpoch:  persisted.AssignmentEpoch,
		SessionId:        persisted.Event.Session,
		OrganizationId:   persisted.Event.Organization,
		EventType:        persisted.Event.Type,
		OccurredAtUnixMs: persisted.Event.Timestamp,
		Payload:          encoded,
	}, nil
}

// persistedEvent keeps the ownership epoch that was current when the event was
// accepted. Replay therefore cannot accidentally relabel a historical event
// with a later assignment.
type persistedEvent struct {
	Event           domain.Event `json:"event"`
	AssignmentEpoch uint64       `json:"assignment_epoch"`
}

// AppendDomainEvent durably records a normalized event only while its current
// desired-state assignment is live. The stored JSON is converted to a protobuf
// Struct at the private control boundary.
func (a ControlAdapter) AppendDomainEvent(
	ctx context.Context,
	event domain.Event,
	assignmentEpoch uint64,
) (Entry, bool, error) {
	payload, err := json.Marshal(persistedEvent{Event: event, AssignmentEpoch: assignmentEpoch})
	if err != nil {
		return Entry{}, false, fmt.Errorf("marshal normalized event: %w", err)
	}
	return a.Journal.Append(ctx, event.ID, payload, time.UnixMilli(event.Timestamp))
}

// EventPayload is the append seam for normalized runtime events. Payload is
// protobuf bytes owned by the event producer; this package never interprets it.
type EventPayload struct {
	EventID    string
	Payload    []byte
	OccurredAt time.Time
}

func (a ControlAdapter) Append(ctx context.Context, event EventPayload) (Entry, bool, error) {
	return a.Journal.Append(ctx, event.EventID, event.Payload, event.OccurredAt)
}
