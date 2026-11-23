package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

type fakeSchedulerSessions struct {
	session domain.WASession
	err     error
}

func (f *fakeSchedulerSessions) Get(_ context.Context, _ string) (domain.WASession, error) {
	return f.session, f.err
}

// fakeCommandStore models the outbox state machine: claims stamp a token,
// every later write is fenced by it, and counters move as in MySQL.
type fakeCommandStore struct {
	inserted   []domain.OutboxEntry
	rows       map[string]*domain.OutboxEntry
	order      []string
	dispatches int
	updates    []struct {
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
	f.order = append(f.order, o.ID)
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

func (f *fakeCommandStore) ClaimDue(_ context.Context, c store.OutboxClaim) ([]domain.OutboxEntry, error) {
	var due []domain.OutboxEntry
	for _, id := range f.order {
		row := f.rows[id]
		claimable := (row.Status == domain.OutboxQueued || row.Status == domain.OutboxAcknowledged) && row.NextAttemptAt <= c.Now ||
			row.Status == domain.OutboxSending && row.LeaseUntil <= c.Now
		if claimable && len(due) < c.Limit {
			row.Status, row.ClaimToken, row.LeaseUntil = domain.OutboxSending, c.Token, c.LeaseUntil
			due = append(due, *row)
		}
	}
	return due, nil
}

func (f *fakeCommandStore) owned(id, token string) (*domain.OutboxEntry, error) {
	row := f.rows[id]
	if row == nil || row.Status != domain.OutboxSending || row.ClaimToken != token {
		return nil, store.ErrOutboxClaimLost
	}
	return row, nil
}

func (f *fakeCommandStore) RenewLease(_ context.Context, id, token string, leaseUntil int64) error {
	row, err := f.owned(id, token)
	if err == nil {
		row.LeaseUntil = leaseUntil
	}
	return err
}

func (f *fakeCommandStore) MarkDispatch(_ context.Context, id, token string, leaseUntil int64) error {
	row, err := f.owned(id, token)
	if err != nil || row.AcknowledgedAt != nil {
		return store.ErrOutboxClaimLost
	}
	f.dispatches++
	row.EffectPossible, row.LeaseUntil = true, leaseUntil
	row.Attempts++
	return nil
}

func (f *fakeCommandStore) ClearDispatchPossible(_ context.Context, id, token string) error {
	row, err := f.owned(id, token)
	if err == nil {
		row.EffectPossible = false
	}
	return err
}

func (f *fakeCommandStore) RecordAcknowledgement(_ context.Context, id, token, waID string, at int64) error {
	row, err := f.owned(id, token)
	if err == nil {
		row.WAMessageID, row.AcknowledgedAt = &waID, &at
	}
	return err
}

func (f *fakeCommandStore) release(row *domain.OutboxEntry, status domain.OutboxStatus, note string, next, now int64) {
	row.Status, row.ClaimToken, row.LeaseUntil = status, "", 0
	row.Error, row.NextAttemptAt, row.UpdatedAt = &note, next, now
}

func (f *fakeCommandStore) Defer(_ context.Context, id, token, note string, next, now int64) error {
	row, err := f.owned(id, token)
	if err != nil {
		return err
	}
	row.Deferrals++
	f.release(row, domain.OutboxQueued, note, next, now)
	return nil
}

func (f *fakeCommandStore) Retry(_ context.Context, id, token, note string, next, now int64) error {
	row, err := f.owned(id, token)
	if err != nil {
		return err
	}
	f.release(row, domain.OutboxQueued, note, next, now)
	return nil
}

func (f *fakeCommandStore) DeferProjection(_ context.Context, id, token, note string, next, now int64) error {
	row, err := f.owned(id, token)
	if err != nil || row.AcknowledgedAt == nil {
		return store.ErrOutboxClaimLost
	}
	row.ProjectionAttempts++
	f.release(row, domain.OutboxAcknowledged, note, next, now)
	return nil
}

func (f *fakeCommandStore) CompleteOwned(_ context.Context, id, token string, status domain.OutboxStatus, waID, note *string, at int64) error {
	row, err := f.owned(id, token)
	if err != nil {
		return err
	}
	row.Status, row.ClaimToken, row.LeaseUntil, row.TerminalAt, row.UpdatedAt = status, "", 0, &at, at
	if waID != nil {
		row.WAMessageID = waID
	}
	row.Error = note
	f.updates = append(f.updates, struct {
		id     string
		status domain.OutboxStatus
	}{id, status})
	return nil
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
		Lease:                 5 * time.Minute,
		Batch:                 8,
		Poll:                  time.Second,
		MaxAttempts:           3,
		MaxProjectionAttempts: 3,
		BackoffBase:           time.Minute,
		BackoffCap:            4 * time.Minute,
		Now: func() time.Time {
			tick++
			return base.Add(time.Duration(tick) * time.Second)
		},
	}
}

func newScheduler(sessions outboundSessionSource, commands outboundCommandStore, engine outboundEngine, limiter outbound.RateLimiter) *OutboundScheduler {
	scheduler, err := NewOutboundScheduler(sessions, commands, engine, limiter, schedulerTestConfig(), nil)
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
	commands := &fakeCommandStore{}
	engine := &fakeEngineSender{}
	limiter := &fakeSchedulerLimiter{ok: false, retryAfter: 30 * time.Second}
	scheduler := newScheduler(sessions, commands, engine, limiter)

	_, err := scheduler.Send(context.Background(), "org_1", "ses_1", textRequest(), outbound.SendOptions{})
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeRateLimited {
		t.Fatalf("want rate limited, got %v", err)
	}
	if len(commands.inserted) != 0 || engine.calls != nil {
		t.Fatal("rate-limited send persisted a row or dispatched")
	}
}

// TestSchedulerValidationErrorIsTerminal pins that deterministic rejections
// (including replayed ledger failures from the gateway) end as 'failed'.
func TestSchedulerValidationErrorIsTerminal(t *testing.T) {
	sessions := &fakeSchedulerSessions{session: schedulerSession()}
	commands := &fakeCommandStore{}
	engine := &fakeEngineSender{errs: []error{domain.ErrValidation("send previously failed: bad media")}, results: []application.SendMessageResult{{}}}
	limiter := &fakeSchedulerLimiter{ok: true}
	scheduler := newScheduler(sessions, commands, engine, limiter)

	_, err := scheduler.Send(context.Background(), "org_1", "ses_1", textRequest(), outbound.SendOptions{})
	if err == nil {
		t.Fatal("want validation error")
	}
	if len(commands.updates) != 1 || commands.updates[0].status != domain.OutboxFailed {
		t.Fatalf("validation failure not terminal: %#v", commands.updates)
	}
}

func queuedCommand(commands *fakeCommandStore, id string) {
	payload, _ := json.Marshal(textRequest())
	_ = commands.Insert(context.Background(), domain.OutboxEntry{
		ID: id, OrganizationID: "org_1", SessionID: "ses_1", Payload: payload, Status: domain.OutboxQueued,
	})
}

// Rate-limit deferrals are counted apart from dispatch attempts: any number
// of them never exhausts the ambiguous-send budget.
func TestSchedulerRateLimitDeferralsDoNotUseDispatchAttempts(t *testing.T) {
	commands := &fakeCommandStore{}
	queuedCommand(commands, "cmd_1")
	limiter := &fakeSchedulerLimiter{ok: false, retryAfter: time.Millisecond}
	engine := &fakeEngineSender{}
	scheduler := newScheduler(&fakeSchedulerSessions{session: schedulerSession()}, commands, engine, limiter)
	for i := 0; i < 10; i++ {
		commands.rows["cmd_1"].NextAttemptAt = 0
		scheduler.processDue(context.Background())
	}
	row := commands.rows["cmd_1"]
	if row.Status != domain.OutboxQueued || row.Deferrals != 10 || row.Attempts != 0 || len(engine.calls) != 0 {
		t.Fatalf("status=%s deferrals=%d attempts=%d calls=%d", row.Status, row.Deferrals, row.Attempts, len(engine.calls))
	}
	// The first real dispatch is attempt one, not attempt eleven.
	limiter.ok = true
	engine.results = []application.SendMessageResult{{}}
	engine.errs = []error{errors.New("gateway unreachable")}
	row.NextAttemptAt = 0
	scheduler.processDue(context.Background())
	if row.Status != domain.OutboxQueued || row.Attempts != 1 {
		t.Fatalf("after ambiguous dispatch: status=%s attempts=%d", row.Status, row.Attempts)
	}
}

// A retry of a command whose effect was already possible still records the
// dispatch through the claim-token fence before calling the engine.
func TestSchedulerRetryDispatchIsFencedByClaimToken(t *testing.T) {
	commands := &fakeCommandStore{}
	queuedCommand(commands, "cmd_1")
	commands.rows["cmd_1"].EffectPossible = true
	commands.rows["cmd_1"].Attempts = 1
	engine := &fakeEngineSender{results: []application.SendMessageResult{{WAMessageID: "WA_1"}}, errs: []error{nil}}
	scheduler := newScheduler(&fakeSchedulerSessions{session: schedulerSession()}, commands, engine,
		&fakeSchedulerLimiter{ok: true})
	scheduler.processDue(context.Background())
	row := commands.rows["cmd_1"]
	if commands.dispatches != 1 || row.Attempts != 2 || row.Status != domain.OutboxSent {
		t.Fatalf("dispatches=%d attempts=%d status=%s", commands.dispatches, row.Attempts, row.Status)
	}

	// A claim that lost its lease cannot dispatch at all.
	queuedCommand(commands, "cmd_2")
	stale := *commands.rows["cmd_2"]
	stale.Status, stale.ClaimToken = domain.OutboxSending, "stale-token"
	commands.rows["cmd_2"].Status, commands.rows["cmd_2"].ClaimToken = domain.OutboxSending, "new-token"
	commands.rows["cmd_2"].LeaseUntil = 1 << 62
	_, err := scheduler.dispatchClaimed(context.Background(), stale, textRequest(), false)
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeConflict || len(engine.calls) != 1 {
		t.Fatalf("stale claim dispatched: err=%v calls=%d", err, len(engine.calls))
	}
}

// A projection that keeps failing after WhatsApp accepted the send parks the
// command as acknowledged with counted attempts, then ends it sent with the
// projection error instead of retrying forever.
func TestSchedulerProjectionRetriesAreCapped(t *testing.T) {
	commands := &fakeCommandStore{}
	queuedCommand(commands, "cmd_1")
	engine := &fakeEngineSender{results: []application.SendMessageResult{{WAMessageID: "WA_1"}}, errs: []error{nil}}
	scheduler := newScheduler(&fakeSchedulerSessions{session: schedulerSession()}, commands, engine,
		&fakeSchedulerLimiter{ok: true})
	scheduler.SetMessageRecorder(&fakeSentRecorder{err: errors.New("messages table unavailable")})
	for i := 0; i < 5; i++ {
		commands.rows["cmd_1"].NextAttemptAt = 0
		scheduler.processDue(context.Background())
		if i == 0 {
			row := commands.rows["cmd_1"]
			if row.Status != domain.OutboxAcknowledged || row.ProjectionAttempts != 1 {
				t.Fatalf("after first projection failure: status=%s projection=%d", row.Status, row.ProjectionAttempts)
			}
		}
	}
	row := commands.rows["cmd_1"]
	if row.Status != domain.OutboxSent || row.ProjectionAttempts != 2 || len(engine.calls) != 1 ||
		row.Error == nil || row.WAMessageID == nil || *row.WAMessageID != "WA_1" {
		t.Fatalf("status=%s projection=%d calls=%d err=%v", row.Status, row.ProjectionAttempts, len(engine.calls), row.Error)
	}
}
