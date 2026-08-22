package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// outboundSessionSource resolves sessions for ownership checks and rate budgets.
type outboundSessionSource interface {
	Get(ctx context.Context, id string) (domain.WASession, error)
}

// outboundCommandStore is the durable command-row boundary the scheduler owns.
type outboundCommandStore interface {
	Insert(ctx context.Context, o domain.OutboxEntry) error
	GetByIdempotency(ctx context.Context, organizationID, idempotencyKey string) (domain.OutboxEntry, error)
	UpdateStatus(ctx context.Context, id string, status domain.OutboxStatus, waMessageID, errMsg *string, updatedAt int64) error
	ClaimDue(ctx context.Context, limit int, dueBefore, staleBefore, updatedAt int64) ([]domain.OutboxEntry, error)
	Reschedule(ctx context.Context, id string, note string, nextAttemptAt, updatedAt int64) (bool, error)
}

// OutboundScheduler is the API-owned send pipeline (Increment 6): it owns
// durable command rows, product rate limits, retry/backoff decisions, and
// dispatch through the private engine. Each outbox row id doubles as the stable
// command_id the executing gateway deduplicates on, so a retried attempt
// re-issues the same identity instead of re-sending.
//
// Ambiguity policy: only deterministic pre-dispatch rejections (surfaced as
// domain validation errors) are terminal failures. Unreachable gateways, stale
// assignments, and lost deadlines return to 'queued' with backoff; a later
// attempt replays the gateway's stored terminal result if one exists.
type OutboundScheduler struct {
	sessions outboundSessionSource
	outbox   outboundCommandStore
	engine   application.MessageSender
	limiter  outbound.RateLimiter
	log      *slog.Logger
	cfg      OutboundSchedulerConfig
}

// OutboundSchedulerConfig is supplied by the composition root; there are no
// implicit defaults because lease sizing must exceed the engine send deadline
// and batch/poll sizing must agree with the deployment's replica count.
type OutboundSchedulerConfig struct {
	Lease       time.Duration // stale-'sending' reclaim window; must exceed the engine send deadline
	Batch       int
	Poll        time.Duration
	MaxAttempts int
	BackoffBase time.Duration
	BackoffCap  time.Duration
	Now         func() time.Time
}

func (c *OutboundSchedulerConfig) normalize() error {
	switch {
	case c.Lease <= 0:
		return fmt.Errorf("outbound scheduler lease must be positive")
	case c.Batch <= 0:
		return fmt.Errorf("outbound scheduler batch must be positive")
	case c.Poll <= 0:
		return fmt.Errorf("outbound scheduler poll must be positive")
	case c.MaxAttempts <= 0:
		return fmt.Errorf("outbound scheduler max attempts must be positive")
	case c.BackoffBase <= 0 || c.BackoffCap < c.BackoffBase:
		return fmt.Errorf("outbound scheduler backoff base/cap invalid")
	case c.Now == nil:
		return fmt.Errorf("outbound scheduler clock is required")
	}
	return nil
}

// NewOutboundScheduler validates its configuration and returns the scheduler.
func NewOutboundScheduler(sessions outboundSessionSource, outbox outboundCommandStore, engine application.MessageSender, limiter outbound.RateLimiter, cfg OutboundSchedulerConfig, log *slog.Logger) (*OutboundScheduler, error) {
	if sessions == nil || outbox == nil || engine == nil {
		return nil, errors.New("outbound scheduler dependencies are required")
	}
	if limiter == nil {
		return nil, errors.New("outbound scheduler rate limiter is required")
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &OutboundScheduler{sessions: sessions, outbox: outbox, engine: engine, limiter: limiter, log: log, cfg: cfg}, nil
}

// Send is the synchronous front door behind MessageService. It preserves the
// legacy §8 contract: idempotency replay before anything else, sync sends
// enforce the rate limit and block on the WhatsApp ack, async sends persist a
// queued command and return accepted.
func (s *OutboundScheduler) Send(ctx context.Context, organizationID, sessionID string, req domain.SendRequest, opts outbound.SendOptions) (outbound.SendResult, error) {
	if err := outbound.Validate(req); err != nil {
		return outbound.SendResult{}, err
	}
	sess, err := s.session(ctx, organizationID, sessionID)
	if err != nil {
		return outbound.SendResult{}, err
	}
	if opts.IdempotencyKey != "" {
		prior, found, err := s.priorByIdempotency(ctx, organizationID, opts.IdempotencyKey)
		if err != nil {
			return outbound.SendResult{}, err
		}
		if found {
			return replayOutboxResult(prior), nil
		}
	}
	if opts.Async {
		return s.enqueueAsync(ctx, sess, req, opts)
	}
	return s.sendSync(ctx, sess, req, opts)
}

func (s *OutboundScheduler) now() time.Time { return s.cfg.Now().UTC() }

func (s *OutboundScheduler) session(ctx context.Context, organizationID, sessionID string) (domain.WASession, error) {
	sess, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return domain.WASession{}, err
	}
	if sess.OrganizationID != organizationID {
		return domain.WASession{}, domain.ErrNotFound("session not found")
	}
	return sess, nil
}

func (s *OutboundScheduler) priorByIdempotency(ctx context.Context, organizationID, key string) (*domain.OutboxEntry, bool, error) {
	prior, err := s.outbox.GetByIdempotency(ctx, organizationID, key)
	if err == nil {
		return &prior, true, nil
	}
	var apiErr *domain.APIError
	if errors.As(err, &apiErr) && apiErr.Code == domain.CodeNotFound {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("idempotency lookup: %w", err)
}

// enqueueAsync persists the queued command and returns accepted. A rate-limit
// breach does not error here: the row simply stays queued for a later attempt
// (§8 deferred behavior).
func (s *OutboundScheduler) enqueueAsync(ctx context.Context, sess domain.WASession, req domain.SendRequest, opts outbound.SendOptions) (outbound.SendResult, error) {
	now := s.now()
	entry := s.newCommand(sess, opts.IdempotencyKey, domain.OutboxQueued, now)
	if err := s.insertCommand(ctx, entry, req); err != nil {
		if opts.IdempotencyKey != "" {
			if prior, found, gerr := s.priorByIdempotency(ctx, sess.OrganizationID, opts.IdempotencyKey); gerr == nil && found {
				return replayOutboxResult(prior), nil
			}
		}
		return outbound.SendResult{}, err
	}
	return outbound.SendResult{Mode: outbound.ModeAsync, OutboxID: entry.ID}, nil
}

// sendSync blocks on the outcome. The command row always exists first, so even
// a keyless send leaves durable evidence and its ambiguity can be reconciled.
func (s *OutboundScheduler) sendSync(ctx context.Context, sess domain.WASession, req domain.SendRequest, opts outbound.SendOptions) (outbound.SendResult, error) {
	ok, retryAfter, err := s.limiter.Allow(ctx, sess.ID, sess.RatePerMin, sess.RatePerHour)
	if err != nil {
		return outbound.SendResult{}, fmt.Errorf("rate check: %w", err)
	}
	if !ok {
		return outbound.SendResult{}, domain.ErrRateLimited("send rate limit exceeded").
			WithDetails(map[string]any{"retryAfterSeconds": int(retryAfter.Seconds())})
	}

	now := s.now()
	entry := s.newCommand(sess, opts.IdempotencyKey, domain.OutboxSending, now)
	entry.Attempts = 1 // this call is attempt one; the lease is stamped by updated_at
	if err := s.insertCommand(ctx, entry, req); err != nil {
		if opts.IdempotencyKey != "" {
			if prior, found, gerr := s.priorByIdempotency(ctx, sess.OrganizationID, opts.IdempotencyKey); gerr == nil && found {
				return replayOutboxResult(prior), nil
			}
		}
		return outbound.SendResult{}, err
	}
	// The limit token was consumed above; dispatch must not consume another.
	return s.dispatchClaimed(ctx, entry, req, false)
}

// newCommand builds one queued-or-sending command row identity.
func (s *OutboundScheduler) newCommand(sess domain.WASession, idempotencyKey string, status domain.OutboxStatus, now time.Time) domain.OutboxEntry {
	return domain.OutboxEntry{
		ID:             domain.NewULID(),
		OrganizationID: sess.OrganizationID,
		SessionID:      sess.ID,
		IdempotencyKey: optionalString(idempotencyKey),
		Status:         status,
		CreatedAt:      now.UnixMilli(),
		UpdatedAt:      now.UnixMilli(),
	}
}

func (s *OutboundScheduler) insertCommand(ctx context.Context, entry domain.OutboxEntry, req domain.SendRequest) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode send payload: %w", err)
	}
	entry.Payload = payload
	return s.outbox.Insert(ctx, entry)
}

// Run claims due commands until ctx is cancelled. Ambiguous failures remain
// incomplete work: they are rescheduled with backoff and retried under the same
// command id, never reported as definite failures while attempts remain.
func (s *OutboundScheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.Poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.processDue(ctx)
		}
	}
}

func (s *OutboundScheduler) processDue(ctx context.Context) {
	now := s.now().UnixMilli()
	entries, err := s.outbox.ClaimDue(ctx, s.cfg.Batch, now, now-s.cfg.Lease.Milliseconds(), now)
	if err != nil {
		s.log.WarnContext(ctx, "claim due outbound commands failed", "err", err)
		return
	}
	for _, entry := range entries {
		var req domain.SendRequest
		if err := json.Unmarshal(entry.Payload, &req); err != nil {
			// A malformed payload can never succeed; recording it terminal keeps
			// it from churning forever.
			note := "malformed stored payload"
			if uerr := s.outbox.UpdateStatus(ctx, entry.ID, domain.OutboxFailed, nil, &note, s.now().UnixMilli()); uerr != nil {
				s.log.WarnContext(ctx, "record malformed outbound command failed", "command", entry.ID, "err", uerr)
			}
			continue
		}
		if _, err := s.dispatchClaimed(ctx, entry, req, true); err != nil {
			s.log.WarnContext(ctx, "dispatch outbound command failed", "command", entry.ID,
				"session", entry.SessionID, "attempt", entry.Attempts, "err", err)
		}
	}
}

// dispatchClaimed drives one leased command to a terminal state or schedules
// its next attempt. checkLimit gates whether this attempt consumes a rate-limit
// token (worker attempts do; a sync front-door attempt already consumed one).
func (s *OutboundScheduler) dispatchClaimed(ctx context.Context, entry domain.OutboxEntry, req domain.SendRequest, checkLimit bool) (outbound.SendResult, error) {
	now := s.now()

	if checkLimit {
		sess, err := s.sessions.Get(ctx, entry.SessionID)
		if err != nil {
			return outbound.SendResult{}, s.ambiguous(ctx, entry, fmt.Sprintf("session lookup: %v", err))
		}
		ok, retryAfter, err := s.limiter.Allow(ctx, entry.SessionID, sess.RatePerMin, sess.RatePerHour)
		if err != nil {
			return outbound.SendResult{}, s.ambiguous(ctx, entry, fmt.Sprintf("rate check: %v", err))
		}
		if !ok {
			if _, rerr := s.outbox.Reschedule(ctx, entry.ID, "rate limited", now.Add(retryAfter).UnixMilli(), now.UnixMilli()); rerr != nil {
				return outbound.SendResult{}, rerr
			}
			return outbound.SendResult{}, domain.ErrRateLimited("send deferred: rate limit").
				WithDetails(map[string]any{"retryAfterSeconds": int(retryAfter.Seconds())})
		}
	}

	result, err := s.engine.SendMessage(ctx, application.SendCommand{
		CommandID:       entry.ID,
		OrganizationID:  entry.OrganizationID,
		SessionID:       entry.SessionID,
		AssignmentEpoch: 0, // the engine client resolves the current assignment target
		Payload:         req,
	})
	timestamp := now.UnixMilli()
	if err == nil && !result.SentAt.IsZero() {
		timestamp = result.SentAt.UnixMilli()
	}
	if err != nil {
		var apiErr *domain.APIError
		if errors.As(err, &apiErr) && apiErr.Code == domain.CodeValidationError {
			// Deterministic rejection (including a replayed prior failure from
			// the gateway ledger): terminal.
			message := err.Error()
			if uerr := s.outbox.UpdateStatus(ctx, entry.ID, domain.OutboxFailed, nil, &message, timestamp); uerr != nil {
				return outbound.SendResult{}, uerr
			}
			return outbound.SendResult{}, err
		}
		return outbound.SendResult{}, s.ambiguous(ctx, entry, err.Error())
	}

	waID := result.WAMessageID
	if uerr := s.outbox.UpdateStatus(ctx, entry.ID, domain.OutboxSent, &waID, nil, timestamp); uerr != nil {
		return outbound.SendResult{}, uerr
	}
	return outbound.SendResult{
		Mode:        outbound.ModeSync,
		WAMessageID: waID,
		Status:      domain.MessageSent,
		Timestamp:   timestamp,
	}, nil
}

// ambiguous reschedules one leased command for a later attempt under the same
// command id, or terminally fails it once attempts are exhausted (the recorded
// error states the ambiguity honestly rather than inventing an outcome).
func (s *OutboundScheduler) ambiguous(ctx context.Context, entry domain.OutboxEntry, cause string) error {
	attempt := entry.Attempts
	if attempt <= 0 {
		attempt = 1
	}
	note := fmt.Sprintf("%s (attempt %d)", cause, attempt)
	if attempt >= s.cfg.MaxAttempts {
		message := fmt.Sprintf("no definite outcome after %d attempts; last error: %s", attempt, cause)
		if uerr := s.outbox.UpdateStatus(ctx, entry.ID, domain.OutboxFailed, nil, &message, s.now().UnixMilli()); uerr != nil {
			return uerr
		}
		return errors.New(note)
	}
	backoff := s.cfg.BackoffBase << (attempt - 1)
	if backoff > s.cfg.BackoffCap || backoff <= 0 {
		backoff = s.cfg.BackoffCap
	}
	next := s.now().Add(backoff)
	if _, err := s.outbox.Reschedule(ctx, entry.ID, note, next.UnixMilli(), s.now().UnixMilli()); err != nil {
		return err
	}
	return errors.New(note)
}

// replayOutboxResult reconstructs a SendResult from a stored row so an
// idempotent replay returns the same shape as the original call. It mirrors the
// legacy sender's replay semantics.
func replayOutboxResult(e *domain.OutboxEntry) outbound.SendResult {
	r := outbound.SendResult{OutboxID: e.ID, Replayed: true}
	switch e.Status {
	case domain.OutboxSent:
		r.Mode = outbound.ModeSync
		r.Status = domain.MessageSent
		if e.WAMessageID != nil {
			r.WAMessageID = *e.WAMessageID
		}
		r.Timestamp = e.UpdatedAt
	case domain.OutboxFailed:
		r.Mode = outbound.ModeSync
		r.Status = domain.MessageFailed
	default: // queued / sending
		r.Mode = outbound.ModeAsync
	}
	return r
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
