package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

type fakeSchedulerSessions struct {
	session domain.WASession
	err     error
}

func (f *fakeSchedulerSessions) Get(_ context.Context, _ string) (domain.WASession, error) {
	return f.session, f.err
}

type fakeCommandStore struct {
	inserted    []domain.OutboxEntry
	rows        map[string]*domain.OutboxEntry
	reschedules []struct {
		id   string
		at   int64
		note string
	}
	updates []struct {
		id     string
		status domain.OutboxStatus
	}
}

func (f *fakeCommandStore) Insert(_ context.Context, o domain.OutboxEntry) error {
	f.inserted = append(f.inserted, o)
	stored := o
	if f.rows == nil {
		f.rows = map[string]*domain.OutboxEntry{}
	}
	f.rows[o.ID] = &stored
	return nil
}

func (f *fakeCommandStore) GetByIdempotency(_ context.Context, _, key string) (domain.OutboxEntry, error) {
	for _, row := range f.rows {
		if row.IdempotencyKey != nil && *row.IdempotencyKey == key {
			return *row, nil
		}
	}
	return domain.OutboxEntry{}, domain.ErrNotFound("outbox entry not found")
}

func (f *fakeCommandStore) UpdateStatus(_ context.Context, id string, status domain.OutboxStatus, waMessageID, errMsg *string, updatedAt int64) error {
	row, ok := f.rows[id]
	if !ok {
		return domain.ErrNotFound("outbox entry not found")
	}
	row.Status = status
	row.UpdatedAt = updatedAt
	if waMessageID != nil {
		row.WAMessageID = waMessageID
	}
	if errMsg != nil {
		row.Error = errMsg
	}
	f.updates = append(f.updates, struct {
		id     string
		status domain.OutboxStatus
	}{id, status})
	return nil
}

func (f *fakeCommandStore) ClaimDue(_ context.Context, limit int, _, _, _ int64) ([]domain.OutboxEntry, error) {
	var due []domain.OutboxEntry
	for _, row := range f.rows {
		if row.Status == domain.OutboxQueued && len(due) < limit {
			due = append(due, *row)
		}
	}
	return due, nil
}

func (f *fakeCommandStore) Reschedule(_ context.Context, id string, note string, nextAttemptAt, updatedAt int64) (bool, error) {
	row, ok := f.rows[id]
	if !ok || row.Status != domain.OutboxSending {
		return false, nil
	}
	row.Status = domain.OutboxQueued
	row.NextAttemptAt = nextAttemptAt
	row.UpdatedAt = updatedAt
	if note != "" {
		row.Error = &note
	}
	f.reschedules = append(f.reschedules, struct {
		id   string
		at   int64
		note string
	}{id, nextAttemptAt, note})
	return true, nil
}

type fakeEngineSender struct {
	results []application.SendMessageResult
	errs    []error
	calls   []application.SendCommand
	opCalls []application.MessageOpCommand
}

func (f *fakeEngineSender) ExecuteOp(_ context.Context, command application.MessageOpCommand) (application.MessageOpResult, error) {
	f.opCalls = append(f.opCalls, command)
	if len(f.errs) > len(f.calls) {
		return application.MessageOpResult{}, f.errs[len(f.calls)]
	}
	return application.MessageOpResult{MutationResult: application.MutationResult{CommandID: command.CommandID}, WAMessageID: "WA_OP"}, nil
}

func (f *fakeEngineSender) SendMessage(_ context.Context, command application.SendCommand) (application.SendMessageResult, error) {
	index := len(f.calls)
	f.calls = append(f.calls, command)
	if index < len(f.results) {
		return f.results[index], f.errs[index]
	}
	return application.SendMessageResult{}, errors.New("no scripted result")
}

type fakeSchedulerLimiter struct {
	ok         bool
	retryAfter time.Duration
	err        error
	calls      int
}

type fakeSentRecorder struct {
	sent []outbound.SentMessage
	err  error
}

func (f *fakeSentRecorder) RecordSent(_ context.Context, message outbound.SentMessage) error {
	f.sent = append(f.sent, message)
	return f.err
}

func (f *fakeSchedulerLimiter) Allow(context.Context, string, int, int) (bool, time.Duration, error) {
	f.calls++
	return f.ok, f.retryAfter, f.err
}

func schedulerTestConfig() OutboundSchedulerConfig {
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	tick := 0
	return OutboundSchedulerConfig{
		Lease:       5 * time.Minute,
		Batch:       8,
		Poll:        time.Second,
		MaxAttempts: 3,
		BackoffBase: time.Minute,
		BackoffCap:  4 * time.Minute,
		Now: func() time.Time {
			tick++
			return base.Add(time.Duration(tick) * time.Second)
		},
	}
}

func newScheduler(sessions outboundSessionSource, store outboundCommandStore, engine outboundEngine, limiter outbound.RateLimiter) *OutboundScheduler {
	scheduler, err := NewOutboundScheduler(sessions, store, engine, limiter, schedulerTestConfig(), nil)
	if err != nil {
		panic(err)
	}
	return scheduler
}

func schedulerSession() domain.WASession {
	return domain.WASession{ID: "ses_1", OrganizationID: "org_1", RatePerMin: 20, RatePerHour: 500}
}

func textRequest() domain.SendRequest {
	return domain.SendRequest{Type: domain.SendTypeText, To: "628123@s.whatsapp.net", Text: "hi"}
}

func TestSchedulerPublishesPollDetails(t *testing.T) {
	scheduler := newScheduler(&fakeSchedulerSessions{session: schedulerSession()}, &fakeCommandStore{},
		&fakeEngineSender{}, &fakeSchedulerLimiter{ok: true})
	scheduler.SetMessageRecorder(&fakeSentRecorder{})
	var payload apitypes.MessagePayload
	scheduler.SetSentEventPublisher(func(_ context.Context, event domain.Event) error {
		payload = event.Payload.(apitypes.MessagePayload)
		return nil
	})
	scheduler.recordSent(context.Background(), "org_1", "ses_1", domain.SendRequest{
		Type:            domain.SendTypePoll,
		To:              "123@g.us",
		Name:            "Lunch?",
		Options:         []string{"Yes", "No"},
		SelectableCount: 1,
		Mentions:        []string{"456@s.whatsapp.net"},
	}, "WA_POLL", 777, "poll-command")
	if payload.Poll == nil || payload.Poll.Name != "Lunch?" || len(payload.Poll.Options) != 2 {
		t.Fatalf("poll payload = %#v", payload)
	}
	if _, ok := payload.Mentions["456@s.whatsapp.net"]; !ok {
		t.Fatalf("mention payload = %#v", payload.Mentions)
	}
}

func TestSchedulerRateLimitedSyncSurfaces429(t *testing.T) {
	sessions := &fakeSchedulerSessions{session: schedulerSession()}
	store := &fakeCommandStore{}
	engine := &fakeEngineSender{}
	limiter := &fakeSchedulerLimiter{ok: false, retryAfter: 30 * time.Second}
	scheduler := newScheduler(sessions, store, engine, limiter)

	_, err := scheduler.Send(context.Background(), "org_1", "ses_1", textRequest(), outbound.SendOptions{})
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeRateLimited {
		t.Fatalf("want rate limited, got %v", err)
	}
	if len(store.inserted) != 0 || engine.calls != nil {
		t.Fatal("rate-limited send persisted a row or dispatched")
	}
}

// TestSchedulerAmbiguousFailureReschedulesWithBackoff pins Increment 6's core
// guarantee: an unavailable gateway returns the command to 'queued' with a
// future attempt under the same command id instead of failing it.
func TestSchedulerAmbiguousFailureReschedulesWithBackoff(t *testing.T) {
	sessions := &fakeSchedulerSessions{session: schedulerSession()}
	store := &fakeCommandStore{}
	engine := &fakeEngineSender{errs: []error{domain.ErrUnavailable("gateway unavailable")}, results: []application.SendMessageResult{{}}}
	limiter := &fakeSchedulerLimiter{ok: true}
	scheduler := newScheduler(sessions, store, engine, limiter)

	_, err := scheduler.Send(context.Background(), "org_1", "ses_1", textRequest(), outbound.SendOptions{})
	if err == nil {
		t.Fatal("sync caller must see the ambiguity error")
	}
	if len(store.reschedules) != 1 {
		t.Fatalf("reschedules = %#v", store.reschedules)
	}
	commandID := store.inserted[0].ID
	if engine.calls[0].CommandID != commandID {
		t.Fatalf("command id changed across retries: %s vs %s", engine.calls[0].CommandID, commandID)
	}
	if store.rows[commandID].Status != domain.OutboxQueued || store.rows[commandID].NextAttemptAt == 0 {
		t.Fatalf("row after ambiguity = %#v", store.rows[commandID])
	}
}

// TestSchedulerExhaustedAmbiguityFailsHonestly verifies the give-up path keeps
// its definite-failure report honest about the missing outcome.
func TestSchedulerExhaustedAmbiguityFailsHonestly(t *testing.T) {
	sessions := &fakeSchedulerSessions{session: schedulerSession()}
	store := &fakeCommandStore{}
	engine := &fakeEngineSender{}
	limiter := &fakeSchedulerLimiter{ok: true}
	scheduler := newScheduler(sessions, store, engine, limiter)

	entry := domain.OutboxEntry{ID: "cmd_x", OrganizationID: "org_1", SessionID: "ses_1", Status: domain.OutboxSending, Attempts: 3}
	store.rows = map[string]*domain.OutboxEntry{"cmd_x": &entry}
	if _, err := scheduler.dispatchClaimed(context.Background(), entry, textRequest(), true); err == nil {
		t.Fatal("expected exhaustion error")
	}
	if len(store.updates) != 1 || store.updates[0].status != domain.OutboxFailed {
		t.Fatalf("updates = %#v", store.updates)
	}
	if !strings.Contains(*store.rows["cmd_x"].Error, "no definite outcome") {
		t.Fatalf("error not honest about ambiguity: %s", *store.rows["cmd_x"].Error)
	}
}

// TestSchedulerValidationErrorIsTerminal pins that deterministic rejections
// (including replayed ledger failures from the gateway) end as 'failed'.
func TestSchedulerValidationErrorIsTerminal(t *testing.T) {
	sessions := &fakeSchedulerSessions{session: schedulerSession()}
	store := &fakeCommandStore{}
	engine := &fakeEngineSender{errs: []error{domain.ErrValidation("send previously failed: bad media")}, results: []application.SendMessageResult{{}}}
	limiter := &fakeSchedulerLimiter{ok: true}
	scheduler := newScheduler(sessions, store, engine, limiter)

	_, err := scheduler.Send(context.Background(), "org_1", "ses_1", textRequest(), outbound.SendOptions{})
	if err == nil {
		t.Fatal("want validation error")
	}
	if len(store.updates) != 1 || store.updates[0].status != domain.OutboxFailed {
		t.Fatalf("validation failure not terminal: %#v", store.updates)
	}
}

// ledgerEngine models the gateway side of the send contract: every CommandID
// reaches WhatsApp at most once, later calls replay the stored terminal result,
// and a transient failure records nothing (so a retry really dispatches).
type ledgerEngine struct {
	calls     []string
	ledger    map[string]application.SendMessageResult
	failFirst error
	successes int
}

func (f *ledgerEngine) ExecuteOp(context.Context, application.MessageOpCommand) (application.MessageOpResult, error) {
	return application.MessageOpResult{}, nil
}

func (f *ledgerEngine) SendMessage(_ context.Context, command application.SendCommand) (application.SendMessageResult, error) {
	f.calls = append(f.calls, command.CommandID)
	if prior, ok := f.ledger[command.CommandID]; ok {
		return prior, nil
	}
	if f.failFirst != nil {
		err := f.failFirst
		f.failFirst = nil
		return application.SendMessageResult{}, err
	}
	f.successes++
	result := application.SendMessageResult{
		MutationResult: application.MutationResult{CommandID: command.CommandID},
		WAMessageID:    fmt.Sprintf("WA_%d", f.successes),
		SentAt:         time.UnixMilli(5000).UTC(),
	}
	if f.ledger == nil {
		f.ledger = map[string]application.SendMessageResult{}
	}
	f.ledger[command.CommandID] = result
	return result, nil
}

// TestSchedulerDuplicateClaimOfSentRowReplaysWithoutSecondSend pins the
// Increment 10 chaos scenario "response lost after WhatsApp success": attempt
// one succeeds and marks the row sent, but the RPC response never reaches the
// scheduler. Stale-lease recovery re-claims the SAME outbox row and dispatches
// again; the gateway ledger replays the stored result, so WhatsApp sees one
// send, the row's terminal state is unchanged, and the retry reports no error.
func TestSchedulerDuplicateClaimOfSentRowReplaysWithoutSecondSend(t *testing.T) {
	sessions := &fakeSchedulerSessions{session: schedulerSession()}
	store := &fakeCommandStore{}
	engine := &ledgerEngine{}
	limiter := &fakeSchedulerLimiter{ok: true}
	scheduler := newScheduler(sessions, store, engine, limiter)

	first, err := scheduler.Send(context.Background(), "org_1", "ses_1", textRequest(), outbound.SendOptions{})
	if err != nil {
		t.Fatalf("initial send: %v", err)
	}
	commandID := store.inserted[0].ID

	// The same row comes back from a stale-lease claim after the lost response.
	entry := *store.rows[commandID]
	second, err := scheduler.dispatchClaimed(context.Background(), entry, textRequest(), true)
	if err != nil {
		t.Fatalf("redispatch of a sent row: %v", err)
	}

	if first.WAMessageID != "WA_1" || second.WAMessageID != first.WAMessageID || second.Timestamp != first.Timestamp {
		t.Fatalf("results diverged: first=%#v second=%#v", first, second)
	}
	if len(engine.calls) != 2 || engine.calls[0] != commandID || engine.calls[1] != commandID {
		t.Fatalf("engine calls = %v, want the same command id twice", engine.calls)
	}
	if engine.successes != 1 {
		t.Fatalf("WhatsApp dispatched %d times, want exactly one send", engine.successes)
	}
	row := store.rows[commandID]
	if row.Status != domain.OutboxSent || row.WAMessageID == nil || *row.WAMessageID != "WA_1" {
		t.Fatalf("terminal row changed: %#v", row)
	}
}

// TestSchedulerAmbiguousTimeoutThenRetryConvergesToSingleSend pins Increment 6's
// §7 exit criterion end to end at the scheduler: an ambiguous timeout leaves the
// command incomplete under the SAME command id, and the later successful retry
// drives it to 'sent' exactly once — no duplicate WhatsApp send, one terminal
// update.
func TestSchedulerAmbiguousTimeoutThenRetryConvergesToSingleSend(t *testing.T) {
	sessions := &fakeSchedulerSessions{session: schedulerSession()}
	store := &fakeCommandStore{}
	engine := &ledgerEngine{failFirst: context.DeadlineExceeded}
	limiter := &fakeSchedulerLimiter{ok: true}
	scheduler := newScheduler(sessions, store, engine, limiter)

	if _, err := scheduler.Send(context.Background(), "org_1", "ses_1", textRequest(), outbound.SendOptions{}); err == nil {
		t.Fatal("ambiguous timeout must surface as an error")
	}
	commandID := store.inserted[0].ID
	row := store.rows[commandID]
	if row.Status != domain.OutboxQueued || row.NextAttemptAt == 0 {
		t.Fatalf("timed-out row not rescheduled: %#v", row)
	}

	// The scheduled retry claims the same row (a real claim bumps attempts).
	retry := *store.rows[commandID]
	retry.Attempts = 2
	result, err := scheduler.dispatchClaimed(context.Background(), retry, textRequest(), true)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if result.Status != domain.MessageSent || result.WAMessageID != "WA_1" {
		t.Fatalf("retry result = %#v", result)
	}
	if len(engine.calls) != 2 || engine.calls[0] != commandID || engine.calls[1] != commandID {
		t.Fatalf("engine calls = %v, want one command id across both attempts", engine.calls)
	}
	if engine.successes != 1 {
		t.Fatalf("WhatsApp dispatched %d times, want exactly one send", engine.successes)
	}
	row = store.rows[commandID]
	if row.Status != domain.OutboxSent || row.WAMessageID == nil || *row.WAMessageID != "WA_1" {
		t.Fatalf("converged row = %#v", row)
	}
	sentUpdates := 0
	for _, update := range store.updates {
		if update.status == domain.OutboxSent {
			sentUpdates++
		}
	}
	if sentUpdates != 1 {
		t.Fatalf("sent updates = %d, want exactly one", sentUpdates)
	}
}
