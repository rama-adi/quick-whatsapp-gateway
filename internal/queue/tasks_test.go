package queue

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/hibiken/asynq"
)

func TestTaskPayloadRoundTrip(t *testing.T) {
	t.Run("outbox-send", func(t *testing.T) {
		task, err := NewOutboxSendTask("out_01HABC")
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if task.Type() != TypeOutboxSend {
			t.Fatalf("type = %q, want %q", task.Type(), TypeOutboxSend)
		}
		got, err := parseOutboxSend(task)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.OutboxID != "out_01HABC" {
			t.Fatalf("outboxId = %q, want out_01HABC", got.OutboxID)
		}
	})

	t.Run("webhook-deliver", func(t *testing.T) {
		task, err := NewWebhookDeliverTask(42)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if task.Type() != TypeWebhookDeliver {
			t.Fatalf("type = %q, want %q", task.Type(), TypeWebhookDeliver)
		}
		got, err := parseWebhookDeliver(task)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.DeliveryID != 42 {
			t.Fatalf("deliveryId = %d, want 42", got.DeliveryID)
		}
	})

	t.Run("retention-prune", func(t *testing.T) {
		task, err := NewRetentionPruneTask(1719400000000)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if task.Type() != TypeRetentionPrune {
			t.Fatalf("type = %q, want %q", task.Type(), TypeRetentionPrune)
		}
		got, err := parseRetentionPrune(task)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.CutoffMs != 1719400000000 {
			t.Fatalf("cutoffMs = %d, want 1719400000000", got.CutoffMs)
		}
	})
}

func TestParsePayloadValidation(t *testing.T) {
	tests := []struct {
		name    string
		parse   func(*asynq.Task) error
		payload string
		wantErr bool
	}{
		{
			name:    "outbox empty id",
			parse:   func(t *asynq.Task) error { _, err := parseOutboxSend(t); return err },
			payload: `{"outboxId":""}`,
			wantErr: true,
		},
		{
			name:    "outbox ok",
			parse:   func(t *asynq.Task) error { _, err := parseOutboxSend(t); return err },
			payload: `{"outboxId":"out_1"}`,
			wantErr: false,
		},
		{
			name:    "outbox malformed json",
			parse:   func(t *asynq.Task) error { _, err := parseOutboxSend(t); return err },
			payload: `{not json`,
			wantErr: true,
		},
		{
			name:    "webhook zero id",
			parse:   func(t *asynq.Task) error { _, err := parseWebhookDeliver(t); return err },
			payload: `{"deliveryId":0}`,
			wantErr: true,
		},
		{
			name:    "webhook ok",
			parse:   func(t *asynq.Task) error { _, err := parseWebhookDeliver(t); return err },
			payload: `{"deliveryId":7}`,
			wantErr: false,
		},
		{
			name:    "retention zero cutoff",
			parse:   func(t *asynq.Task) error { _, err := parseRetentionPrune(t); return err },
			payload: `{"cutoffMs":0}`,
			wantErr: true,
		},
		{
			name:    "retention negative cutoff",
			parse:   func(t *asynq.Task) error { _, err := parseRetentionPrune(t); return err },
			payload: `{"cutoffMs":-5}`,
			wantErr: true,
		},
		{
			name:    "retention ok",
			parse:   func(t *asynq.Task) error { _, err := parseRetentionPrune(t); return err },
			payload: `{"cutoffMs":1}`,
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Build a raw task directly so we can inject malformed/edge payloads.
			task := asynq.NewTask("x", []byte(tc.payload))
			err := tc.parse(task)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// Verify the JSON field names are stable on the wire (other phases may inspect
// the payload), not just self-consistent on round-trip.
func TestPayloadJSONShape(t *testing.T) {
	b, _ := json.Marshal(OutboxSendPayload{OutboxID: "out_1"})
	if string(b) != `{"outboxId":"out_1"}` {
		t.Fatalf("outbox json = %s", b)
	}
	b, _ = json.Marshal(WebhookDeliverPayload{DeliveryID: 9})
	if string(b) != `{"deliveryId":9}` {
		t.Fatalf("webhook json = %s", b)
	}
	b, _ = json.Marshal(RetentionPrunePayload{CutoffMs: 123})
	if string(b) != `{"cutoffMs":123}` {
		t.Fatalf("retention json = %s", b)
	}
}

func TestSkipRetryWrappingHelper(t *testing.T) {
	// Sanity: the wrapping pattern the handlers use for bad payloads stays
	// detectable via errors.Is so asynq won't retry malformed tasks.
	err := errors.Join(asynq.SkipRetry, errors.New("bad payload"))
	if !errors.Is(err, asynq.SkipRetry) {
		t.Fatal("expected SkipRetry to be detectable")
	}
}
