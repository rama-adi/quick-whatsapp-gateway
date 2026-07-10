package outbound

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
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
	quotes   QuoteResolver
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

// WithQuoteResolver wires lookup of reply targets so outgoing messages can carry
// WhatsApp-native quote metadata.
func WithQuoteResolver(r QuoteResolver) SenderOption {
	return func(s *Sender) { s.quotes = r }
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

	if err := s.pace(ctx); err != nil {
		if outboxID != "" {
			s.updateOutboxStatus(ctx, outboxID, domain.OutboxFailed, "", err.Error())
		}
		return SendResult{}, err
	}

	waID, ts, err := s.dispatch(ctx, req)
	if err != nil {
		if outboxID != "" {
			s.updateOutboxStatus(ctx, outboxID, domain.OutboxFailed, "", err.Error())
		}
		s.log.ErrorContext(ctx, "outbound sync send failed",
			"session", sess.ID, "type", req.Type, "err", err)
		return SendResult{}, err
	}

	if outboxID != "" {
		s.updateOutboxStatus(ctx, outboxID, domain.OutboxSent, waID, "")
	}
	return SendResult{
		Mode:        ModeSync,
		WAMessageID: waID,
		Status:      domain.MessageSent,
		Timestamp:   ts,
	}, nil
}

func (s *Sender) updateOutboxStatus(ctx context.Context, id string, status domain.OutboxStatus, waID, message string) {
	// The WhatsApp attempt has already reached an outcome. Give its bookkeeping
	// a short detached window so a disconnected HTTP client cannot leave an
	// idempotency row permanently stuck in "sending".
	bookkeepingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.outbox.UpdateStatus(bookkeepingCtx, id, status, waID, message); err != nil {
		s.log.ErrorContext(bookkeepingCtx, "outbound: update outbox status failed",
			"outbox", id, "status", status, "err", err)
	}
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
// status, returning the populated entry. For a media send the inline bytes are
// stored here only transiently: the async worker needs them to perform the
// upload, and the store strips them from the row once the send reaches a terminal
// state (see store.OutboxRepo.UpdateStatus) so the file content is not retained.
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
func (s *Sender) pace(ctx context.Context) error {
	if s.pacing <= 0 {
		return nil
	}
	d := time.Duration(s.rng() * float64(s.pacing))
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// dispatch routes a validated request to the right WAClient call, then records
// the sent message to the messages table. It is the single chokepoint both the
// sync front-door and the async worker (via the exported Dispatch) funnel
// through, so recording here covers every successful send exactly once.
func (s *Sender) dispatch(ctx context.Context, req domain.SendRequest) (waMessageID string, ts int64, err error) {
	quote := s.resolveQuote(ctx, req)
	switch req.Type {
	case domain.SendTypeText:
		waMessageID, ts, err = s.wa.SendText(ctx, req.To, req.Text, quote, req.Mentions)
	case domain.SendTypePoll:
		waMessageID, ts, err = s.wa.SendPoll(ctx, req.To, req.Name, req.Options, req.SelectableCount, req.PollEndTime, req.PollHideVotes)
	case domain.SendTypeLocation:
		waMessageID, ts, err = s.wa.SendLocation(ctx, req.To, req.Latitude, req.Longitude, req.Name)
	case domain.SendTypeContact:
		var name, phone, vcard string
		if req.Contact != nil {
			name, phone, vcard = req.Contact.Name, req.Contact.Phone, req.Contact.VCard
		}
		waMessageID, ts, err = s.wa.SendContact(ctx, req.To, name, phone, vcard)
	case domain.SendTypeImage, domain.SendTypeVideo, domain.SendTypeAudio, domain.SendTypeDocument, domain.SendTypeSticker:
		var data []byte
		var mimetype string
		data, mimetype, err = resolveMedia(ctx, req.Media)
		if err != nil {
			return "", 0, err
		}
		caption, filename := mediaCaptionFilename(req.Media)
		waMessageID, ts, err = s.wa.SendMedia(ctx, req.To, req.Type, data, mimetype, caption, filename, quote, req.Mentions)
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

func (s *Sender) resolveQuote(ctx context.Context, req domain.SendRequest) QuoteInfo {
	if req.ReplyTo == "" {
		return QuoteInfo{}
	}
	quote := QuoteInfo{ID: req.ReplyTo}
	if s.quotes == nil {
		return quote
	}
	sessionID := SessionIDFromContext(ctx)
	if sessionID == "" {
		return quote
	}
	msg, err := s.quotes.GetByWAID(ctx, sessionID, req.ReplyTo)
	if err != nil {
		s.log.WarnContext(ctx, "outbound: resolve quoted message failed",
			"session", sessionID, "replyTo", req.ReplyTo, "err", err)
		return quote
	}
	quote.ChatJID = msg.ChatJID
	quote.Type = msg.Type
	if msg.Body != nil {
		quote.Body = *msg.Body
	}
	if msg.FromMe {
		quote.FromMe = true
		quote.SenderJID = ""
	} else if msg.SenderJID != nil && *msg.SenderJID != "" {
		quote.SenderJID = *msg.SenderJID
	} else if msg.SenderLID != nil {
		quote.SenderJID = *msg.SenderLID
	}
	return quote
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
	mediaMeta, hasMedia := outboundMedia(req)
	if err := s.recorder.RecordSent(ctx, SentMessage{
		SessionID:           sessionID,
		WAMessageID:         waMessageID,
		ChatJID:             req.To,
		Type:                req.Type,
		Body:                outboundBody(req),
		ReplyTo:             req.ReplyTo,
		Mentions:            req.Mentions,
		HasMedia:            hasMedia,
		MediaMeta:           mediaMeta,
		PollOptions:         req.Options,
		PollSelectableCount: req.SelectableCount,
		PollEndTime:         req.PollEndTime,
		PollHideVotes:       req.PollHideVotes,
		TimestampMs:         ts,
	}); err != nil {
		s.log.WarnContext(ctx, "outbound: record sent message failed",
			"session", sessionID, "waMessageId", waMessageID, "err", err)
	}
}

// outboundBody is the human-readable body stored for a send: the text for a text
// message, the question for a poll, the label for a location, the caption for
// media; empty otherwise (the type column carries the rest).
func outboundBody(req domain.SendRequest) string {
	switch req.Type {
	case domain.SendTypeText:
		return req.Text
	case domain.SendTypePoll, domain.SendTypeLocation:
		return req.Name
	default:
		if isMediaType(req.Type) && req.Media != nil {
			return req.Media.Caption
		}
		return ""
	}
}

// isMediaType reports whether a send type carries a media file.
func isMediaType(t string) bool {
	switch t {
	case domain.SendTypeImage, domain.SendTypeVideo, domain.SendTypeAudio, domain.SendTypeDocument, domain.SendTypeSticker:
		return true
	}
	return false
}

// outboundMedia derives the media descriptor recorded on the messages row for a
// media send (best-effort metadata; URL media size is only known during dispatch).
func outboundMedia(req domain.SendRequest) (*domain.MediaMeta, bool) {
	if !isMediaType(req.Type) || req.Media == nil {
		return nil, false
	}
	size := int64(0)
	if strings.TrimSpace(req.Media.Data) != "" {
		size = int64(approxDecodedLen(req.Media.Data))
	}
	return &domain.MediaMeta{
		Mimetype: req.Media.Mimetype,
		Size:     size,
		Filename: req.Media.Filename,
	}, true
}

// mediaCaptionFilename pulls the caption + filename off a media payload (nil-safe).
func mediaCaptionFilename(m *domain.MediaPayload) (caption, filename string) {
	if m == nil {
		return "", ""
	}
	return m.Caption, m.Filename
}

// resolveMedia returns the bytes for a media send, from base64 data or a URL.
func resolveMedia(ctx context.Context, m *domain.MediaPayload) ([]byte, string, error) {
	if m == nil {
		return nil, "", domain.ErrValidation("media is required")
	}
	if strings.TrimSpace(m.URL) != "" {
		return fetchMediaURL(ctx, m)
	}
	return decodeMedia(m)
}

// decodeMedia decodes the base64 payload of a media send into raw bytes and
// returns the declared mimetype. It accepts a bare base64 string or a data: URI
// (pulling the mimetype from the URI when one isn't given), tolerates padded or
// raw base64, and enforces MaxMediaBytes. Decode/size failures are validation
// errors (400), never 500s.
func decodeMedia(m *domain.MediaPayload) ([]byte, string, error) {
	if m == nil || strings.TrimSpace(m.Data) == "" {
		return nil, "", domain.ErrValidation("media.data (base64) is required when media.url is not provided")
	}
	raw := strings.TrimSpace(m.Data)
	mimetype := m.Mimetype
	if strings.HasPrefix(raw, "data:") {
		if i := strings.IndexByte(raw, ','); i >= 0 {
			header := raw[len("data:"):i] // e.g. "image/png;base64"
			raw = raw[i+1:]
			if mimetype == "" {
				if j := strings.IndexByte(header, ';'); j >= 0 {
					mimetype = header[:j]
				} else {
					mimetype = header
				}
			}
		}
	}
	data, err := decodeBase64(raw)
	if err != nil {
		return nil, "", domain.ErrValidation("media.data must be valid base64")
	}
	if len(data) == 0 {
		return nil, "", domain.ErrValidation("media.data decoded to empty")
	}
	if len(data) > MaxMediaBytes {
		return nil, "", domain.ErrValidation(fmt.Sprintf("media exceeds the %d byte limit", MaxMediaBytes))
	}
	return data, mimetype, nil
}

func fetchMediaURL(ctx context.Context, m *domain.MediaPayload) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(m.URL), nil)
	if err != nil {
		return nil, "", domain.ErrValidation("media.url must be a valid http(s) URL")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", domain.ErrValidation("media.url could not be downloaded")
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", domain.ErrValidation(fmt.Sprintf("media.url returned HTTP %d", resp.StatusCode))
	}
	if resp.ContentLength > MaxMediaBytes {
		return nil, "", domain.ErrValidation(fmt.Sprintf("media exceeds the %d byte limit", MaxMediaBytes))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxMediaBytes+1))
	if err != nil {
		return nil, "", domain.ErrValidation("media.url could not be read")
	}
	if len(body) == 0 {
		return nil, "", domain.ErrValidation("media.url downloaded an empty file")
	}
	if len(body) > MaxMediaBytes {
		return nil, "", domain.ErrValidation(fmt.Sprintf("media exceeds the %d byte limit", MaxMediaBytes))
	}
	mimetype := m.Mimetype
	if mimetype == "" {
		mimetype = resp.Header.Get("Content-Type")
		if i := strings.IndexByte(mimetype, ';'); i >= 0 {
			mimetype = strings.TrimSpace(mimetype[:i])
		}
	}
	return body, mimetype, nil
}

// decodeBase64 accepts both standard (padded) and raw (unpadded) base64.
func decodeBase64(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
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

	if err := s.pace(ctx); err != nil {
		return SendResult{}, err
	}

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
