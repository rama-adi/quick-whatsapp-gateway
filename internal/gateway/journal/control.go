package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
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

	mu          sync.RWMutex
	assignments map[string]eventAssignment
	revision    uint64
}

type eventAssignment struct {
	organization string
	epoch        uint64
}

// ResetDesiredAssignments pauses replay until this connection receives an
// authoritative complete snapshot. A reconnect must not reuse stale ownership.
func (a *ControlAdapter) ResetDesiredAssignments() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.assignments = nil
}

// SetDesiredAssignments records only successfully applied control-plane state.
// STOP assignments still own their terminal events; omission or a new epoch is
// the authority to retire queued events that the API can no longer accept.
func (a *ControlAdapter) SetDesiredAssignments(snapshot *gatewayv1.DesiredStateSnapshot) {
	assignments := make(map[string]eventAssignment, len(snapshot.Assignments))
	for _, item := range snapshot.Assignments {
		assignments[item.SessionId] = eventAssignment{organization: item.OrganizationId, epoch: item.AssignmentEpoch}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if snapshot.Revision < a.revision {
		return // delayed snapshots cannot retire events for a newer assignment
	}
	a.revision = snapshot.Revision
	a.assignments = assignments
}

func (a *ControlAdapter) Batch(
	ctx context.Context,
	maxEntries int,
	maxBytes int64,
	gatewayID string,
	connectionEpoch uint64,
	metadata func([]byte) (*gatewayv1.GatewayEvent, error),
) (*gatewayv1.GatewayEventBatch, error) {
	return a.batch(ctx, maxEntries, maxBytes, gatewayID, connectionEpoch, metadata, nil)
}

func (a *ControlAdapter) batch(
	ctx context.Context,
	maxEntries int,
	maxBytes int64,
	gatewayID string,
	connectionEpoch uint64,
	metadata func([]byte) (*gatewayv1.GatewayEvent, error),
	assignments map[string]eventAssignment,
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
		if assignments != nil {
			assignment, owned := assignments[event.SessionId]
			if !owned || assignment.organization != event.OrganizationId || assignment.epoch != event.AssignmentEpoch {
				continue
			}
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
	// A normal API ACK removes skipped entries preceding the last sent event.
	// When every entry is retired, advance locally under snapshot authority so
	// deleted sessions cannot permanently block later journal entries.
	if len(batch.Events) == 0 && len(entries) > 0 && assignments != nil {
		if err := a.Journal.Ack(ctx, entries[len(entries)-1].Seq); err != nil {
			return nil, err
		}
	}
	return batch, nil
}

func (a *ControlAdapter) Ack(ctx context.Context, sequence uint64) error {
	return a.Journal.Ack(ctx, sequence)
}

func (a *ControlAdapter) AckEvents(ctx context.Context, sequence uint64) error {
	return a.Ack(ctx, sequence)
}

func (a *ControlAdapter) NextEventBatch(ctx context.Context, epoch uint64) (*gatewayv1.GatewayEventBatch, error) {
	a.mu.RLock()
	assignments := a.assignments
	a.mu.RUnlock()
	if assignments == nil {
		return &gatewayv1.GatewayEventBatch{}, nil
	}
	return a.batch(ctx, DefaultBatchEntries, DefaultBatchBytes, a.GatewayID, epoch, decodeJournalEvent, assignments)
}

// decodeJournalEvent converts one stored journal payload into the private
// control-stream event representation.
func decodeJournalEvent(payload []byte) (*gatewayv1.GatewayEvent, error) {
	var persisted persistedEvent
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, fmt.Errorf("decode journal event: %w", err)
	}
	missingIdentity := persisted.Event.ID == "" || persisted.AssignmentEpoch == 0
	missingOwnership := persisted.Event.Session == "" || persisted.Event.Organization == ""
	missingMetadata := missingIdentity || missingOwnership
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
func (a *ControlAdapter) AppendDomainEvent(
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

func (a *ControlAdapter) Append(ctx context.Context, event EventPayload) (Entry, bool, error) {
	return a.Journal.Append(ctx, event.EventID, event.Payload, event.OccurredAt)
}
