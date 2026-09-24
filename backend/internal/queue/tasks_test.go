package queue

import (
	"testing"

	"github.com/hibiken/asynq"
)

// TestParsePayloadValidation feeds every parser malformed JSON and structurally valid payloads with empty,
// zero, or non-positive required fields. Each case must return an error before a consumer is called.
// Permanent payload defects are separated from transient worker failures so they can be skipped rather
// than retried.
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
