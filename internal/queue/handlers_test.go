package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"
)

// fakeConsumers records what the handlers delegated to it, and can be made to
// return an error to exercise the failure path.
type fakeConsumers struct {
	outboxID   string
	deliveryID uint64
	cutoffMs   int64
	outboxN    int
	webhookN   int
	pruneN     int
	failErr    error
}

func (f *fakeConsumers) ProcessOutbox(_ context.Context, outboxID string) error {
	f.outboxN++
	f.outboxID = outboxID
	return f.failErr
}

func (f *fakeConsumers) DeliverWebhook(_ context.Context, deliveryID uint64) error {
	f.webhookN++
	f.deliveryID = deliveryID
	return f.failErr
}

func (f *fakeConsumers) Prune(_ context.Context, cutoffMs int64) error {
	f.pruneN++
	f.cutoffMs = cutoffMs
	return f.failErr
}

// dispatch resolves the registered handler for a task via the mux and invokes it,
// exactly as the asynq Server would but without a running Redis/server.
func dispatch(mux *asynq.ServeMux, task *asynq.Task) error {
	h, _ := mux.Handler(task)
	return h.ProcessTask(context.Background(), task)
}

func TestHandlerDispatch(t *testing.T) {
	fake := &fakeConsumers{}
	mux := Handlers{Outbox: fake, Webhooks: fake, Retention: fake}.Mux()

	outboxTask, _ := NewOutboxSendTask("out_xyz")
	if err := dispatch(mux, outboxTask); err != nil {
		t.Fatalf("outbox dispatch: %v", err)
	}
	if fake.outboxN != 1 || fake.outboxID != "out_xyz" {
		t.Fatalf("outbox not delegated: n=%d id=%q", fake.outboxN, fake.outboxID)
	}

	webhookTask, _ := NewWebhookDeliverTask(99)
	if err := dispatch(mux, webhookTask); err != nil {
		t.Fatalf("webhook dispatch: %v", err)
	}
	if fake.webhookN != 1 || fake.deliveryID != 99 {
		t.Fatalf("webhook not delegated: n=%d id=%d", fake.webhookN, fake.deliveryID)
	}

	pruneTask, _ := NewRetentionPruneTask(555)
	if err := dispatch(mux, pruneTask); err != nil {
		t.Fatalf("prune dispatch: %v", err)
	}
	if fake.pruneN != 1 || fake.cutoffMs != 555 {
		t.Fatalf("prune not delegated: n=%d cutoff=%d", fake.pruneN, fake.cutoffMs)
	}
}

func TestHandlerPropagatesConsumerError(t *testing.T) {
	sentinel := errors.New("boom")
	fake := &fakeConsumers{failErr: sentinel}
	mux := Handlers{Outbox: fake, Webhooks: fake, Retention: fake}.Mux()

	task, _ := NewOutboxSendTask("out_1")
	err := dispatch(mux, task)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel wrapped, got %v", err)
	}
	// A consumer failure must NOT be marked SkipRetry — asynq should retry.
	if errors.Is(err, asynq.SkipRetry) {
		t.Fatal("consumer error should be retryable, not SkipRetry")
	}
}

func TestHandlerMalformedPayloadSkipsRetry(t *testing.T) {
	fake := &fakeConsumers{}
	mux := Handlers{Outbox: fake}.Mux()

	// Build a task of the right type but with a payload that fails validation.
	bad := asynq.NewTask(TypeOutboxSend, []byte(`{"outboxId":""}`))
	err := dispatch(mux, bad)
	if err == nil {
		t.Fatal("expected error for empty outboxId")
	}
	if !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("malformed payload should SkipRetry, got %v", err)
	}
	if fake.outboxN != 0 {
		t.Fatal("consumer should not be called on bad payload")
	}
}

func TestMuxOnlyRegistersProvidedConsumers(t *testing.T) {
	// Only outbox is provided; webhook/retention tasks must hit NotFoundHandler.
	mux := Handlers{Outbox: &fakeConsumers{}}.Mux()

	webhookTask, _ := NewWebhookDeliverTask(1)
	if err := dispatch(mux, webhookTask); err == nil {
		t.Fatal("expected not-found error for unregistered webhook handler")
	}

	pruneTask, _ := NewRetentionPruneTask(1)
	if err := dispatch(mux, pruneTask); err == nil {
		t.Fatal("expected not-found error for unregistered retention handler")
	}
}

func TestRetentionCutoffMs(t *testing.T) {
	now := time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		days   int
		wantOK bool
	}{
		{days: 0, wantOK: false},
		{days: -3, wantOK: false},
		{days: 7, wantOK: true},
	}
	for _, tc := range tests {
		cutoff, ok := RetentionCutoffMs(now, tc.days)
		if ok != tc.wantOK {
			t.Fatalf("days=%d ok=%v want %v", tc.days, ok, tc.wantOK)
		}
		if ok {
			want := now.Add(-time.Duration(tc.days) * 24 * time.Hour).UnixMilli()
			if cutoff != want {
				t.Fatalf("days=%d cutoff=%d want %d", tc.days, cutoff, want)
			}
			if cutoff >= now.UnixMilli() {
				t.Fatalf("cutoff %d should be before now %d", cutoff, now.UnixMilli())
			}
		}
	}
}
