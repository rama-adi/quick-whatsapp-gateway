package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"
)

// fakeConsumers records what the handler delegated to it, and can be made to
// return an error to exercise the failure path.
type fakeConsumers struct {
	cutoffMs int64
	pruneN   int
	failErr  error
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

// TestHandlerDispatch resolves a retention task through the ServeMux; the
// decoded cutoff must reach the consumer.
func TestHandlerDispatch(t *testing.T) {
	fake := &fakeConsumers{}
	mux := Handlers{Retention: fake}.Mux()
	pruneTask, _ := NewRetentionPruneTask(555)
	if err := dispatch(mux, pruneTask); err != nil {
		t.Fatalf("prune dispatch: %v", err)
	}
	if fake.pruneN != 1 || fake.cutoffMs != 555 {
		t.Fatalf("prune not delegated: n=%d cutoff=%d", fake.pruneN, fake.cutoffMs)
	}
}

// TestHandlerMalformedPayloadSkipsRetry: a malformed payload is wrapped with
// asynq.SkipRetry and never reaches the consumer.
func TestHandlerMalformedPayloadSkipsRetry(t *testing.T) {
	fake := &fakeConsumers{}
	mux := Handlers{Retention: fake}.Mux()
	bad := asynq.NewTask(TypeRetentionPrune, []byte(`{"cutoffMs":0}`))
	err := dispatch(mux, bad)
	if err == nil || !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("malformed payload should SkipRetry, got %v", err)
	}
	if fake.pruneN != 0 {
		t.Fatal("consumer should not be called on bad payload")
	}
}

// TestMuxOnlyRegistersProvidedConsumers: without a retention consumer the
// task type is unregistered.
func TestMuxOnlyRegistersProvidedConsumers(t *testing.T) {
	mux := Handlers{}.Mux()
	pruneTask, _ := NewRetentionPruneTask(1)
	if err := dispatch(mux, pruneTask); err == nil {
		t.Fatal("expected not-found error for unregistered retention handler")
	}
}

// TestRetentionCutoffMs computes cutoffs for positive, zero, and negative retention-day settings relative
// to a fixed instant. Positive days subtract exact 24-hour periods; non-positive values return ok=false to
// mean keep forever. This keeps policy interpretation out of the pruning worker.
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
