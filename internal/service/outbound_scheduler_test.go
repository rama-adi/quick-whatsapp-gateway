package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
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

func newScheduler(sessions outboundSessionSource, store outboundCommandStore, engine application.MessageSender, limiter outbound.RateLimiter) *OutboundScheduler {
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

// TestSchedulerSyncSendRecordsTerminalCommand pins the front-door path: the
// durable row exists before dispatch and lands terminal 'sent' with the WhatsApp id.
func TestSchedulerSyncSendRecordsTerminalCommand(t *testing.T) {
	sessions := &fakeSchedulerSessions{session: schedulerSession()}
	store := &fakeCommandStore{}
	engine := &fakeEngineSender{results: []application.SendMessageResult{{MutationResult: application.MutationResult{CommandID: "cmd"}, WAMessageID: "WA_1", SentAt: time.UnixMilli(777).UTC()}}, errs: []error{nil}}
	limiter := &fakeSchedulerLimiter{ok: true}
	scheduler := newScheduler(sessions, store, engine, limiter)

	result, err := scheduler.Send(context.Background(), "org_1", "ses_1", textRequest(), outbound.SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result.Mode != outbound.ModeSync || result.WAMessageID != "WA_1" || result.Timestamp != 777 {
		t.Fatalf("result = %#v", result)
	}
	if len(store.inserted) != 1 || store.inserted[0].Status != domain.OutboxSending {
		t.Fatalf("rows = %#v", store.inserted)
	}
	if len(store.updates) != 1 || store.updates[0].status != domain.OutboxSent {
		t.Fatalf("updates = %#v", store.updates)
	}
	// The engine received the row id as its stable command id.
	if len(engine.calls) != 1 || engine.calls[0].CommandID != store.inserted[0].ID {
		t.Fatalf("engine commands = %#v rows = %#v", engine.calls, store.inserted)
	}
}

// TestSchedulerReplaysIdempotencyKeyWithoutDispatch verifies §8 replay.
func TestSchedulerReplaysIdempotencyKeyWithoutDispatch(t *testing.T) {
	sessions := &fakeSchedulerSessions{session: schedulerSession()}
	waID := "WA_ORIGINAL"
	store := &fakeCommandStore{rows: map[string]*domain.OutboxEntry{
		"cmd_1": {ID: "cmd_1", OrganizationID: "org_1", SessionID: "ses_1", Status: domain.OutboxSent, WAMessageID: &waID, UpdatedAt: 555, IdempotencyKey: optionalString("key-1")},
	}}
	engine := &fakeEngineSender{}
	limiter := &fakeSchedulerLimiter{ok: true}
	scheduler := newScheduler(sessions, store, engine, limiter)

	result, err := scheduler.Send(context.Background(), "org_1", "ses_1", textRequest(), outbound.SendOptions{IdempotencyKey: "key-1"})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !result.Replayed || result.WAMessageID != "WA_ORIGINAL" || result.Timestamp != 555 {
		t.Fatalf("replay result = %#v", result)
	}
	if engine.calls != nil || limiter.calls != 0 {
		t.Fatalf("replay dispatched or rate-checked: %#v", engine.calls)
	}
}

// TestSchedulerRateLimitedSyncSurfaces429 pins the sync over-limit contract.
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

// TestSchedulerAsyncPersistsQueuedCommand verifies the async contract.
func TestSchedulerAsyncPersistsQueuedCommand(t *testing.T) {
	sessions := &fakeSchedulerSessions{session: schedulerSession()}
	store := &fakeCommandStore{}
	engine := &fakeEngineSender{}
	limiter := &fakeSchedulerLimiter{}
	scheduler := newScheduler(sessions, store, engine, limiter)

	result, err := scheduler.Send(context.Background(), "org_1", "ses_1", textRequest(), outbound.SendOptions{Async: true})
	if err != nil {
		t.Fatalf("async Send: %v", err)
	}
	if result.Mode != outbound.ModeAsync || result.OutboxID == "" {
		t.Fatalf("result = %#v", result)
	}
	if len(store.inserted) != 1 || store.inserted[0].Status != domain.OutboxQueued || engine.calls != nil {
		t.Fatalf("async persisted=%#v engine=%#v", store.inserted, engine.calls)
	}
	// Payload round-trips through JSON for later attempts.
	var stored domain.SendRequest
	if err := json.Unmarshal(store.inserted[0].Payload, &stored); err != nil || stored.Text != "hi" {
		t.Fatalf("payload = %s (%v)", store.inserted[0].Payload, err)
	}
}
