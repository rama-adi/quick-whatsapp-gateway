package service

import (
	"context"
	"errors"
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
