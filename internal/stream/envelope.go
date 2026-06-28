package stream

// This file holds the shared event-envelope helpers used by the transport-agnostic
// pump (pump.go): building the connected/ping frames, rendering replayed event-log
// entries into the §9 envelope shape, peeking an event's id/type off a raw payload,
// and the heartbeat duration helper. The NDJSON wire framing that once lived here
// is gone — realtime is WebSocket-only (the router redeems a ticket and pumps
// discrete JSON messages); these helpers carry no transport-specific framing.

import (
	"encoding/json"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// resumeReplayLimit caps how many event-log entries a single ?since= resume
// replays before switching to the live tail, bounding memory/latency on a client
// that has been disconnected for a long time. Older history beyond this is the
// concern of the REST event-log endpoints, not the live stream.
const resumeReplayLimit = 1000

// connectedEnvelope is the first frame emitted on a freshly-opened stream, before
// any replay or live tail. It confirms the stream is live immediately — clients
// otherwise wait up to one heartbeat interval for the first byte — and advertises
// the heartbeat cadence so a client can size its own dead-stream timeout.
func connectedEnvelope(heartbeatSecs int) map[string]any {
	return map[string]any{
		"event":            "connected",
		"timestamp":        domain.NowMs(),
		"heartbeatSeconds": heartbeatSecs,
	}
}

// pingEnvelope is the heartbeat frame shape (§9: {"event":"ping",...}). It carries
// a fresh epoch-ms timestamp so clients can measure liveness.
func pingEnvelope() map[string]any {
	return map[string]any{
		"event":     "ping",
		"timestamp": domain.NowMs(),
	}
}

// logEntryEnvelope renders a replayed event-log entry into the §9 envelope shape,
// matching what the live publisher emits for the same event. EventLogEntry's JSON
// tags already map id/event/session/organization/payload, but it has no "schema" and
// uses createdAt rather than timestamp, so we build the envelope explicitly to
// keep replayed and live frames byte-compatible in shape.
func logEntryEnvelope(e *domain.EventLogEntry) domain.Event {
	return domain.Event{
		Schema:       domain.Schema,
		ID:           e.EventID,
		Type:         e.Type,
		Session:      e.SessionID,
		Organization: e.OrganizationID,
		Timestamp:    e.CreatedAt,
		Payload:      json.RawMessage(e.Payload),
	}
}

// peekEvent extracts the id and event type from a raw published envelope without
// fully decoding the payload. Returns ok=false if the JSON is malformed or both
// fields are absent.
func peekEvent(payload string) (id, eventType string, ok bool) {
	var head struct {
		ID   string `json:"id"`
		Type string `json:"event"`
	}
	if err := json.Unmarshal([]byte(payload), &head); err != nil {
		return "", "", false
	}
	// A valid envelope always carries an event type; id may legitimately be empty
	// only for synthetic frames, but published domain events always set it.
	if head.Type == "" {
		return "", "", false
	}
	return head.ID, head.Type, true
}

// durationSeconds converts a positive integer second count to a time.Duration.
func durationSeconds(secs int) time.Duration {
	return time.Duration(secs) * time.Second
}
