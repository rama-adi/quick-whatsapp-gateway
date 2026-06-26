package stream

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// lineWriter writes one JSON object per line (NDJSON) to an http.ResponseWriter
// and flushes after each line so chunks reach the client immediately.
type lineWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func newLineWriter(w http.ResponseWriter, f http.Flusher) *lineWriter {
	return &lineWriter{w: w, flusher: f}
}

// writeJSON marshals v and writes it as a single NDJSON line (object + '\n'),
// then flushes.
func (l *lineWriter) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return l.writeRaw(data)
}

// writeRaw writes pre-marshalled JSON as one NDJSON line. The payload must be a
// single JSON value with no interior newlines (json.Marshal output satisfies
// this); we append exactly one trailing '\n' as the line terminator, normalizing
// any newline already present so we never emit a blank line.
func (l *lineWriter) writeRaw(data []byte) error {
	data = bytes.TrimRight(data, "\n")
	if _, err := l.w.Write(data); err != nil {
		return err
	}
	if _, err := l.w.Write(newline); err != nil {
		return err
	}
	l.flusher.Flush()
	return nil
}

var newline = []byte{'\n'}

// pingEnvelope is the heartbeat line shape (§9: {"event":"ping",...}). It carries
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
// keep replayed and live lines byte-compatible in shape.
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
