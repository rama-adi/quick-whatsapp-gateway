// Package outbound runs the outbound pipeline (masterplan §8): the unified typed
// send, idempotency keys, per-session Redis rate limiting, and the sync/async
// outbox split.
//
// The pipeline never imports sibling internal packages. Every collaborator (the
// whatsmeow client, the outbox repo, the rate limiter, the clock) is a small
// CONSUMER INTERFACE defined here and satisfied by concrete types wired in by
// the composition root (Go convention: interfaces are defined by the consumer). The real
// whatsmeow adapter lives in waclient.go and is the one place allowed to import
// whatsmeow.
package outbound

import (
	"context"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// SendResult is the outcome of a send. For sync sends Mode is "sync" and
// WAMessageID/Status/Timestamp are populated from the whatsmeow ack. For async
// sends Mode is "async" and only OutboxID is populated (the final status arrives
// later via a message.status event when the outbox worker drains the row).
type SendResult struct {
	// Mode is "sync" or "async".
	Mode string `json:"mode"`
	// WAMessageID is the WhatsApp message id assigned by whatsmeow (sync only).
	WAMessageID string `json:"waMessageId,omitempty"`
	// Status is the message status after the ack (sync only). For successful
	// sends this is domain.MessageSent.
	Status domain.MessageStatus `json:"status,omitempty"`
	// Timestamp is the server timestamp of the send, epoch-ms (sync only).
	Timestamp int64 `json:"timestamp,omitempty"`
	// OutboxID is the persisted outbox row id (async only).
	OutboxID string `json:"outboxId,omitempty"`
	// Replayed is true when this result was returned from a prior send with the
	// same Idempotency-Key (no new WhatsApp call was made).
	Replayed bool `json:"replayed,omitempty"`
}

const (
	ModeSync  = "sync"
	ModeAsync = "async"
)

// SendOptions carries the per-request knobs that are not part of the message
// body itself: the sync/async mode and the optional idempotency key.
type SendOptions struct {
	// Async, when true, persists the request to the outbox and returns an
	// outbox id immediately instead of blocking on the WhatsApp ack (§8).
	Async bool
	// IdempotencyKey is the organization-scoped Idempotency-Key header value. When
	// non-empty, a replay with the same key returns the original result without
	// re-sending (§8).
	IdempotencyKey string
}

// ---------------------------------------------------------------------------
// Consumer interfaces (defined by this package; implemented elsewhere).
// ---------------------------------------------------------------------------

// WAClient is the narrow slice of whatsmeow the send pipeline needs. Each method
// returns the assigned WhatsApp message id and the server timestamp in epoch-ms.
// The Sender never touches whatsmeow protobufs directly — it speaks this
// interface, and waclient.go provides the real adapter over *whatsmeow.Client
// using the recon §7 Build* helpers + SendMessage.
type WAClient interface {
	// SendText sends a plain/extended text message. replyTo is the quoted
	// wa_message_id ("" for none); mentions are mentioned JID strings.
	SendText(ctx context.Context, to, text string, quote QuoteInfo, mentions []string) (waMessageID string, ts int64, err error)
	// SendPoll creates a poll. endTime is epoch-ms when non-zero; hideVotes asks
	// WhatsApp to hide participant names in the poll vote list.
	SendPoll(ctx context.Context, to, name string, options []string, selectableCount int, endTime int64, hideVotes bool) (waMessageID string, ts int64, err error)
	// SendLocation sends a location pin.
	SendLocation(ctx context.Context, to string, lat, lon float64, name string) (waMessageID string, ts int64, err error)
	// SendContact sends a contact card. vcard, when non-empty, is sent verbatim;
	// otherwise the adapter builds one from name/phone.
	SendContact(ctx context.Context, to, name, phone, vcard string) (waMessageID string, ts int64, err error)
	// SendMedia uploads media bytes to WhatsApp and sends the matching message
	// kind. mediaType is a domain.SendType* media constant (image/video/audio/
	// document/sticker); mimetype is detected from the bytes when ""; caption
	// (image/video/document), filename (document), replyTo and mentions are
	// optional (replyTo is a quoted wa_message_id).
	SendMedia(ctx context.Context, to, mediaType string, data []byte, mimetype, caption, filename string, quote QuoteInfo, mentions []string) (waMessageID string, ts int64, err error)
	// SendAlbum sends one WhatsApp album container followed by its associated
	// image/video children. The returned id is the album container id.
	SendAlbum(ctx context.Context, to, caption string, medias []AlbumMedia, quote QuoteInfo, mentions []string) (waMessageID string, ts int64, err error)

	// React adds (or, with emoji=="", removes) a reaction to a message
	// (BuildReaction). chat is the target chat JID; sender is the original
	// message sender JID ("" for your own outgoing message); msgID is the target.
	React(ctx context.Context, chat, sender, msgID, emoji string) (waMessageID string, ts int64, err error)
	// Edit replaces the text of a previously sent message (BuildEdit).
	Edit(ctx context.Context, chat, msgID, newText string) (waMessageID string, ts int64, err error)
	// Revoke deletes a message for everyone (BuildRevoke). sender is the original
	// sender JID ("" for your own message).
	Revoke(ctx context.Context, chat, sender, msgID string) (waMessageID string, ts int64, err error)
	// Vote casts a poll vote on the given poll message (BuildPollVote).
	Vote(ctx context.Context, pollChat, pollSender, pollMsgID string, options []string) (waMessageID string, ts int64, err error)
	// Forward forwards an existing message to a destination chat. The adapter
	// builds a forwarded-context message; sourceChat/sourceSender/sourceMsgID
	// identify the original.
	Forward(ctx context.Context, to, sourceChat, sourceSender, sourceMsgID string) (waMessageID string, ts int64, err error)
}

// AlbumMedia is one fully resolved image/video ready for upload.
type AlbumMedia struct {
	Type     string
	Data     []byte
	Mimetype string
}

// QuoteInfo is the WhatsApp quote context attached to outbound sends.
// ID is always the quoted wa_message_id; the other fields are best-effort
// enrichments resolved from the local message store.
type QuoteInfo struct {
	ID        string
	ChatJID   string
	SenderJID string
	Type      string
	Body      string
	// FromMe marks a quoted message this session sent itself. SenderJID is left
	// empty for those (the store has no sender columns for outbound rows); the
	// adapter fills in the session's own JID for group sends, where an explicit
	// participant is required for correct attribution.
	FromMe bool
}

// Empty reports whether no quote was requested.
func (q QuoteInfo) Empty() bool { return q.ID == "" }

// OutboxRepo is the persistence boundary for the async outbox table (§5). The
// MySQL implementation in the store package satisfies it.
type OutboxRepo interface {
	// Insert persists a queued outbox row. Implementations MUST enforce the
	// (organization_id, idempotency_key) uniqueness constraint and return an error
	// wrapping domain.CodeConflict (or *domain.APIError with that code) on a
	// duplicate so the caller can fall back to GetByIdempotencyKey.
	Insert(ctx context.Context, e *domain.OutboxEntry) error
	// GetByIdempotencyKey returns the existing outbox row for (organizationID, key),
	// or (nil, nil) when none exists.
	GetByIdempotencyKey(ctx context.Context, organizationID, key string) (*domain.OutboxEntry, error)
	// UpdateStatus transitions an outbox row and records the wa_message_id and
	// error (both optional). It bumps updated_at.
	UpdateStatus(ctx context.Context, id string, status domain.OutboxStatus, waMessageID, errMsg string) error
	// ClaimQueued atomically claims up to limit queued rows for a session,
	// flipping them to 'sending', for the async worker to drain.
	ClaimQueued(ctx context.Context, sessionID string, limit int) ([]*domain.OutboxEntry, error)
}

// MessageRecorder persists a row in the messages table for a successfully
// dispatched outbound send, so the gateway's own sends ("bot messages") show up
// in chat history alongside inbound messages. whatsmeow does NOT echo a
// self-authored send back as an events.Message on the same device, so without
// this the inbound pipeline never sees it and the only trace is the transient
// outbox row. Recording is best-effort (the Sender logs and swallows errors —
// the WhatsApp send already succeeded) and optional (wired via
// WithMessageRecorder; nil disables it). The store implementation upserts keyed
// by (session_id, wa_message_id), the same key the inbound pipeline uses, so a
// later echo or receipt reconciles onto the same row instead of duplicating it.
type MessageRecorder interface {
	RecordSent(ctx context.Context, m SentMessage) error
}

// QuoteResolver resolves a reply target from the message store so WhatsApp gets
// the participant + quoted payload it needs to render a native quote.
type QuoteResolver interface {
	GetByWAID(ctx context.Context, sessionID, waMessageID string) (domain.Message, error)
}

// SentMessage is the content-bearing slice of a successful send the recorder
// needs to write a from_me/direction=out/status=sent messages row. Sender/ack
// fields (session id, wa message id, timestamp) are filled by the Sender from
// the dispatch result and request context.
type SentMessage struct {
	SessionID           string
	WAMessageID         string
	ChatJID             string            // the recipient JID (req.To)
	Type                string            // one of the domain.SendType* constants
	Body                string            // text body / poll question / location label / media caption ("" for none)
	ReplyTo             string            // quoted wa_message_id ("" for none)
	Mentions            []string          // mentioned JIDs
	HasMedia            bool              // true for media sends (image, …)
	MediaMeta           *domain.MediaMeta // media descriptor (mimetype/size/filename) when HasMedia
	PollOptions         []string          // poll creation options, in order
	PollSelectableCount int               // poll max selections
	PollEndTime         int64             // poll close time, epoch-ms
	PollHideVotes       bool              // hide participant names in votes
	TimestampMs         int64             // server timestamp of the send, epoch-ms
}

// RateLimiter enforces the per-session send budget (rate_per_min / rate_per_hour,
// §8). Allow consumes one token from both windows atomically; it returns ok=false
// (with a retryAfter hint) when either window is exhausted. A Redis-backed
// implementation lives in ratelimit.go.
type RateLimiter interface {
	Allow(ctx context.Context, sessionID string, perMin, perHour int) (ok bool, retryAfter time.Duration, err error)
}

// Clock is the time boundary, injected so tests are deterministic. NowMs returns
// epoch-ms (domain.NowMs in production).
type Clock interface {
	NowMs() int64
}

// systemClock is the production Clock.
type systemClock struct{}

func (systemClock) NowMs() int64 { return domain.NowMs() }

// SystemClock returns the real wall-clock Clock.
func SystemClock() Clock { return systemClock{} }
