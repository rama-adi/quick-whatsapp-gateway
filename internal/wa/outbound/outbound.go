// Package outbound runs the outbound pipeline (masterplan §8): the unified typed
// send, idempotency keys, per-session Redis rate limiting, and the sync/async
// outbox split.
//
// The pipeline never imports sibling internal packages. Every collaborator (the
// whatsmeow client, the outbox repo, the rate limiter, the clock) is a small
// CONSUMER INTERFACE defined here and satisfied by concrete types wired in by
// Phase 3 (Go convention: interfaces are defined by the consumer). The real
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
	SendText(ctx context.Context, to, text, replyTo string, mentions []string) (waMessageID string, ts int64, err error)
	// SendPoll creates a poll (BuildPollCreation + SendMessage).
	SendPoll(ctx context.Context, to, name string, options []string, selectableCount int) (waMessageID string, ts int64, err error)
	// SendLocation sends a location pin.
	SendLocation(ctx context.Context, to string, lat, lon float64, name string) (waMessageID string, ts int64, err error)
	// SendContact sends a contact card. vcard, when non-empty, is sent verbatim;
	// otherwise the adapter builds one from name/phone.
	SendContact(ctx context.Context, to, name, phone, vcard string) (waMessageID string, ts int64, err error)

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

// OutboxRepo is the persistence boundary for the async outbox table (§5). Phase 3
// wires the MySQL implementation from the store package.
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
