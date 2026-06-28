package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// Sender is the unified outbound pipeline (§8). It validates and routes a
// domain.SendRequest, enforces idempotency and per-session rate limiting, then
// either blocks on the WhatsApp ack (sync) or persists to the outbox (async).
type Sender struct {
	wa       WAClient
	outbox   OutboxRepo
	limits   RateLimiter
	clock    Clock
	recorder MessageRecorder
	log      *slog.Logger

	// pacing, when > 0, applies a random sleep in [0, pacing) before each sync
	// send to mimic human cadence (the optional "jittered pacing" of §8).
	pacing time.Duration
	// rng is the source for jitter; injectable so tests are deterministic.
	rng func() float64
}

// SenderOption configures a Sender.
type SenderOption func(*Sender)

// WithPacing enables jittered pacing: a uniform random delay in [0, max) before
// each sync send. Zero (the default) disables it.
func WithPacing(max time.Duration) SenderOption {
	return func(s *Sender) { s.pacing = max }
}

// WithLogger sets the structured logger (defaults to slog.Default()).
func WithLogger(l *slog.Logger) SenderOption {
	return func(s *Sender) { s.log = l }
}

// WithMessageRecorder wires the recorder that writes each successful send into
// the messages table (the gateway's own "bot messages"). Without it, sends are
// tracked only in the outbox and never appear in chat history.
func WithMessageRecorder(r MessageRecorder) SenderOption {
	return func(s *Sender) { s.recorder = r }
}

// withRand overrides the jitter RNG (test hook).
func withRand(f func() float64) SenderOption {
	return func(s *Sender) { s.rng = f }
}

// NewSender constructs a Sender. All four collaborators are required.
func NewSender(wa WAClient, outbox OutboxRepo, limits RateLimiter, clock Clock, opts ...SenderOption) *Sender {
	s := &Sender{
		wa:     wa,
		outbox: outbox,
		limits: limits,
		clock:  clock,
		log:    slog.Default(),
		rng:    rand.Float64,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Send runs the pipeline for one request against one session.
//
// Order of operations (§8):
//  1. Validate the request (type + per-type required fields). Media types are
//     rejected up front with a 501 (not_implemented).
//  2. Idempotency: if a key is supplied and a prior outbox row exists for
//     (organization, key), replay its stored result without re-sending.
//  3. Async path: persist a queued outbox row and return its id (202-style).
//     Rate-limit breaches here defer (the row stays queued) rather than error.
//  4. Sync path: enforce the rate limit (429-style error on breach), optionally
//     pace, then dispatch to whatsmeow and return the ack.
//
// For sync sends with an idempotency key, the result is recorded in the outbox
// (status sent/failed) so a later replay returns it.
func (s *Sender) Send(ctx context.Context, sess domain.WASession, req domain.SendRequest, opts SendOptions) (SendResult, error) {
	if err := validate(req); err != nil {
		return SendResult{}, err
	}

	// Carry the target session id so a session-routing WAClient can resolve the
	// right per-session whatsmeow client for this request.
	ctx = WithSessionID(ctx, sess.ID)

	// 2. Idempotency replay (applies to both modes).
	if opts.IdempotencyKey != "" {
		if prior, err := s.outbox.GetByIdempotencyKey(ctx, sess.OrganizationID, opts.IdempotencyKey); err != nil {
			return SendResult{}, fmt.Errorf("outbound: idempotency lookup: %w", err)
		} else if prior != nil {
			return replayResult(prior), nil
		}
	}

	if opts.Async {
		return s.sendAsync(ctx, sess, req, opts)
	}
	return s.sendSync(ctx, sess, req, opts)
}

// sendSync enforces the rate limit, optionally paces, dispatches, and (when an
// idempotency key is present) records the outcome to the outbox for replay.
func (s *Sender) sendSync(ctx context.Context, sess domain.WASession, req domain.SendRequest, opts SendOptions) (SendResult, error) {
	ok, retryAfter, err := s.limits.Allow(ctx, sess.ID, sess.RatePerMin, sess.RatePerHour)
	if err != nil {
		return SendResult{}, fmt.Errorf("outbound: rate check: %w", err)
	}
	if !ok {
		// §8 + open-decision #3: sync over-limit returns a rate_limited error.
		return SendResult{}, domain.ErrRateLimited("send rate limit exceeded").
			WithDetails(map[string]any{"retryAfterSeconds": int(retryAfter.Seconds())})
	}

	// Optionally persist a 'sending' outbox row first so a concurrent replay
	// with the same key sees a row (and so the result is durably recorded).
	var outboxID string
	if opts.IdempotencyKey != "" {
		entry, err := s.persistOutbox(ctx, sess, req, opts.IdempotencyKey, domain.OutboxSending)
		if err != nil {
			// A duplicate key here means another in-flight send already claimed
			// it; fall back to replaying the stored row.
			if prior, gerr := s.outbox.GetByIdempotencyKey(ctx, sess.OrganizationID, opts.IdempotencyKey); gerr == nil && prior != nil {
				return replayResult(prior), nil
			}
			return SendResult{}, err
		}
		outboxID = entry.ID
	}

	s.pace()

	waID, ts, err := s.dispatch(ctx, req)
	if err != nil {
		if outboxID != "" {
			_ = s.outbox.UpdateStatus(ctx, outboxID, domain.OutboxFailed, "", err.Error())
		}
		s.log.ErrorContext(ctx, "outbound sync send failed",
			"session", sess.ID, "type", req.Type, "err", err)
		return SendResult{}, err
	}

	if outboxID != "" {
		_ = s.outbox.UpdateStatus(ctx, outboxID, domain.OutboxSent, waID, "")
	}
	return SendResult{
		Mode:        ModeSync,
		WAMessageID: waID,
		Status:      domain.MessageSent,
		Timestamp:   ts,
	}, nil
}

// sendAsync persists a queued outbox row and returns its id. The async worker
// drains it later; a rate-limit breach does NOT error here — the row simply
// stays queued to be retried (the "deferred" behavior of §8).
func (s *Sender) sendAsync(ctx context.Context, sess domain.WASession, req domain.SendRequest, opts SendOptions) (SendResult, error) {
	entry, err := s.persistOutbox(ctx, sess, req, opts.IdempotencyKey, domain.OutboxQueued)
	if err != nil {
		if opts.IdempotencyKey != "" {
			if prior, gerr := s.outbox.GetByIdempotencyKey(ctx, sess.OrganizationID, opts.IdempotencyKey); gerr == nil && prior != nil {
				return replayResult(prior), nil
			}
		}
		return SendResult{}, err
	}
	return SendResult{Mode: ModeAsync, OutboxID: entry.ID}, nil
}

// persistOutbox marshals the request and inserts an outbox row with the given
// status, returning the populated entry.
func (s *Sender) persistOutbox(ctx context.Context, sess domain.WASession, req domain.SendRequest, idemKey string, status domain.OutboxStatus) (*domain.OutboxEntry, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("outbound: marshal payload: %w", err)
	}
	now := s.clock.NowMs()
	entry := &domain.OutboxEntry{
		ID:             domain.NewOutboxID(),
		OrganizationID: sess.OrganizationID,
		SessionID:      sess.ID,
		Payload:        payload,
		Status:         status,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if idemKey != "" {
		entry.IdempotencyKey = &idemKey
	}
	if err := s.outbox.Insert(ctx, entry); err != nil {
		return nil, fmt.Errorf("outbound: insert outbox: %w", err)
	}
	return entry, nil
}

// pace applies jittered pacing if enabled.
func (s *Sender) pace() {
	if s.pacing <= 0 {
		return
	}
	d := time.Duration(s.rng() * float64(s.pacing))
	if d > 0 {
		time.Sleep(d)
	}
}

// dispatch routes a validated request to the right WAClient call, then records
// the sent message to the messages table. Media types never reach here (validate
// rejects them). It is the single chokepoint both the sync front-door and the
// async worker (via the exported Dispatch) funnel through, so recording here
// covers every successful send exactly once.
func (s *Sender) dispatch(ctx context.Context, req domain.SendRequest) (waMessageID string, ts int64, err error) {
	switch req.Type {
	case domain.SendTypeText:
		waMessageID, ts, err = s.wa.SendText(ctx, req.To, req.Text, req.ReplyTo, req.Mentions)
	case domain.SendTypePoll:
		waMessageID, ts, err = s.wa.SendPoll(ctx, req.To, req.Name, req.Options, req.SelectableCount)
	case domain.SendTypeLocation:
		waMessageID, ts, err = s.wa.SendLocation(ctx, req.To, req.Latitude, req.Longitude, req.Name)
	case domain.SendTypeContact:
		var name, phone, vcard string
		if req.Contact != nil {
			name, phone, vcard = req.Contact.Name, req.Contact.Phone, req.Contact.VCard
		}
		waMessageID, ts, err = s.wa.SendContact(ctx, req.To, name, phone, vcard)
	default:
		// Unreachable for validated requests; guard anyway.
		return "", 0, domain.ErrValidation(fmt.Sprintf("unsupported send type %q", req.Type))
	}
	if err != nil {
		return "", 0, err
	}
	s.recordSent(ctx, req, waMessageID, ts)
	return waMessageID, ts, nil
}

// recordSent best-effort persists a messages-table row for a successful send
// (from_me, direction=out, status=sent). A recorder failure is logged, never
// returned: the WhatsApp send already succeeded and must not be reported as
// failed (which would also flip the outbox row to failed and trip a needless
// retry). No-op when no recorder is wired or the session id / message id is
// unknown (e.g. a unit-test dispatch with a fake client).
func (s *Sender) recordSent(ctx context.Context, req domain.SendRequest, waMessageID string, ts int64) {
	if s.recorder == nil || waMessageID == "" {
		return
	}
	sessionID := SessionIDFromContext(ctx)
	if sessionID == "" {
		return
	}
	if ts == 0 {
		ts = s.clock.NowMs()
	}
	if err := s.recorder.RecordSent(ctx, SentMessage{
		SessionID:   sessionID,
		WAMessageID: waMessageID,
		ChatJID:     req.To,
		Type:        req.Type,
		Body:        outboundBody(req),
		ReplyTo:     req.ReplyTo,
		Mentions:    req.Mentions,
		TimestampMs: ts,
	}); err != nil {
		s.log.WarnContext(ctx, "outbound: record sent message failed",
			"session", sessionID, "waMessageId", waMessageID, "err", err)
	}
}

// outboundBody is the human-readable body stored for a send: the text for a text
// message, the question for a poll, the label for a location; empty otherwise
// (the type column carries the rest).
func outboundBody(req domain.SendRequest) string {
	switch req.Type {
	case domain.SendTypeText:
		return req.Text
	case domain.SendTypePoll, domain.SendTypeLocation:
		return req.Name
	default:
		return ""
	}
}

// Dispatch is the exported entry point the async outbox worker uses to drive a
// persisted, already-validated request to whatsmeow. It does NOT apply rate
// limiting or idempotency (those are the synchronous front-door's job); it is a
// thin, reusable router around the WAClient.
func (s *Sender) Dispatch(ctx context.Context, req domain.SendRequest) (waMessageID string, ts int64, err error) {
	if err := validate(req); err != nil {
		return "", 0, err
	}
	return s.dispatch(ctx, req)
}

// SendOp executes a message sub-resource operation (reaction/edit/revoke/vote/
// forward, §11) against a session. Ops are always synchronous (they act on an
// existing message and return immediately) and are rate-limited like sends.
func (s *Sender) SendOp(ctx context.Context, sess domain.WASession, req OpRequest) (SendResult, error) {
	if err := validateOp(req); err != nil {
		return SendResult{}, err
	}

	// Carry the target session id for the session-routing WAClient.
	ctx = WithSessionID(ctx, sess.ID)

	ok, retryAfter, err := s.limits.Allow(ctx, sess.ID, sess.RatePerMin, sess.RatePerHour)
	if err != nil {
		return SendResult{}, fmt.Errorf("outbound: rate check: %w", err)
	}
	if !ok {
		return SendResult{}, domain.ErrRateLimited("send rate limit exceeded").
			WithDetails(map[string]any{"retryAfterSeconds": int(retryAfter.Seconds())})
	}

	s.pace()

	var waID string
	var ts int64
	switch req.Op {
	case OpReaction:
		waID, ts, err = s.wa.React(ctx, req.Chat, req.Sender, req.MsgID, req.Emoji)
	case OpEdit:
		waID, ts, err = s.wa.Edit(ctx, req.Chat, req.MsgID, req.NewText)
	case OpRevoke:
		waID, ts, err = s.wa.Revoke(ctx, req.Chat, req.Sender, req.MsgID)
	case OpVote:
		waID, ts, err = s.wa.Vote(ctx, req.Chat, req.Sender, req.MsgID, req.Options)
	case OpForward:
		waID, ts, err = s.wa.Forward(ctx, req.To, req.Chat, req.Sender, req.MsgID)
	default:
		return SendResult{}, domain.ErrValidation(fmt.Sprintf("unknown message op %q", req.Op))
	}
	if err != nil {
		s.log.ErrorContext(ctx, "outbound message op failed",
			"session", sess.ID, "op", req.Op, "err", err)
		return SendResult{}, err
	}
	return SendResult{
		Mode:        ModeSync,
		WAMessageID: waID,
		Status:      domain.MessageSent,
		Timestamp:   ts,
	}, nil
}

// replayResult reconstructs a SendResult from a stored outbox row so an
// idempotent replay returns the same shape as the original call.
func replayResult(e *domain.OutboxEntry) SendResult {
	r := SendResult{OutboxID: e.ID, Replayed: true}
	switch e.Status {
	case domain.OutboxSent:
		r.Mode = ModeSync
		r.Status = domain.MessageSent
		if e.WAMessageID != nil {
			r.WAMessageID = *e.WAMessageID
		}
		r.Timestamp = e.UpdatedAt
	case domain.OutboxFailed:
		r.Mode = ModeSync
		r.Status = domain.MessageFailed
	default: // queued / sending
		r.Mode = ModeAsync
	}
	return r
}

// IsRateLimited reports whether err is the pipeline's rate_limited error.
func IsRateLimited(err error) bool {
	var apiErr *domain.APIError
	return errors.As(err, &apiErr) && apiErr.Code == domain.CodeRateLimited
}
