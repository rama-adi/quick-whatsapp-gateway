// Package webhooks implements the webhook dispatcher (masterplan §9, webhooks
// half): it persists a pending delivery per matching webhook when the inbound
// pipeline produces a domain.Event (the Enqueuer), then a Dispatcher worker
// loop claims due deliveries and POSTs the event body to each endpoint with the
// X-Webhook-* headers, retries per domain.RetryPolicy, dedups by event_id, and
// dead-letters exhausted deliveries.
//
// Collaborators are expressed as small CONSUMER interfaces defined here (Go
// convention: interfaces defined by the consumer). Phase 3 wires concrete
// MySQL repos, an *http.Client, a real clock, and the AES-GCM decryptor in.
package webhooks

import (
	"context"
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// Webhook delivery HTTP headers (§9).
const (
	HeaderRequestID     = "X-Webhook-Request-Id"
	HeaderTimestamp     = "X-Webhook-Timestamp"
	HeaderHMAC          = "X-Webhook-Hmac"
	HeaderHMACAlgorithm = "X-Webhook-Hmac-Algorithm"
	HMACAlgorithm       = "sha512"
)

// WebhookRepo reads the configured webhooks that match an incoming event. The
// implementation (Phase 3) applies tenant/session scoping and the active flag in
// SQL; ListMatching returns only webhooks whose events list matches eventType
// (the dispatcher re-checks with EventMatches as a defensive guard, but the repo
// is the primary filter so we don't load every webhook into memory).
type WebhookRepo interface {
	// ListMatching returns active webhooks for the tenant whose session scope
	// covers session (session_id == session OR session_id IS NULL) and whose
	// events list matches eventType ("*" or contains eventType).
	ListMatching(ctx context.Context, tenant, session, eventType string) ([]domain.Webhook, error)
	// Get loads a single webhook by id. The dispatcher needs the URL, custom
	// headers, hmac secret and retry policy when sending a claimed delivery
	// (the delivery row only carries webhook_id + event_id). Implementations
	// should return a domain error (domain.ErrNotFound) when the webhook is
	// gone so the dispatcher can dead-letter the orphaned delivery.
	Get(ctx context.Context, id string) (domain.Webhook, error)
}

// EventStore loads the persisted event body to POST. The fan-out stage appends
// every event to event_log (§7); the delivery row references it by event_id, so
// the dispatcher reloads the envelope here rather than carrying the (potentially
// large) payload through the deliveries table.
type EventStore interface {
	// GetEvent loads the event envelope by its exposed event id (event_log.event_id).
	GetEvent(ctx context.Context, eventID string) (domain.Event, error)
}

// WebhookDeliveryRepo owns the webhook_deliveries table lifecycle.
type WebhookDeliveryRepo interface {
	// Create inserts a pending delivery row. The caller fills WebhookID,
	// EventID, Status=pending, Attempts=0, NextRetryAt (when it first becomes
	// due) and CreatedAt.
	Create(ctx context.Context, d *domain.WebhookDelivery) error
	// ClaimDue atomically claims up to limit deliveries that are pending/failed
	// and due (next_retry_at <= now), returning them for sending. The
	// implementation should mark them in-flight (e.g. via SELECT ... FOR UPDATE
	// SKIP LOCKED or a CAS on a claim column) so concurrent dispatchers don't
	// double-send.
	ClaimDue(ctx context.Context, now int64, limit int) ([]domain.WebhookDelivery, error)
	// MarkDelivered transitions a delivery to delivered, recording the HTTP
	// response code and bumping attempts.
	MarkDelivered(ctx context.Context, id uint64, attempts int, responseCode int) error
	// MarkFailed transitions a delivery to failed and schedules nextRetryAt,
	// bumping attempts and recording the response code (nil when no response)
	// and last error.
	MarkFailed(ctx context.Context, id uint64, attempts int, nextRetryAt int64, responseCode *int, lastErr string) error
	// MarkDead transitions a delivery to dead (retries exhausted), bumping
	// attempts and recording the last response code and error.
	MarkDead(ctx context.Context, id uint64, attempts int, responseCode *int, lastErr string) error
	// ExistsTerminal reports whether a delivery for this webhook_id+event_id is
	// already in a terminal state (delivered or dead) — used for dedup so the
	// same event is not re-enqueued/re-sent after success or dead-lettering.
	ExistsTerminal(ctx context.Context, webhookID, eventID string) (bool, error)
}

// HTTPDoer is the minimal HTTP client surface the dispatcher needs. *http.Client
// satisfies it; Phase 3 injects one configured with the per-request timeout.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Clock yields the current epoch-ms time. Injected so tests can use a fixed
// clock and retry-backoff math is deterministic.
type Clock interface {
	NowMs() int64
}

// Decryptor decrypts an at-rest secret (the AES-GCM encrypted webhook HMAC
// secret stored in webhooks.hmac_secret). Defined as a consumer interface so the
// crypto implementation lives elsewhere; the dispatcher only needs Decrypt.
type Decryptor interface {
	Decrypt(ciphertext []byte) (plaintext []byte, err error)
}

// systemClock is the production Clock backed by domain.NowMs.
type systemClock struct{}

func (systemClock) NowMs() int64 { return domain.NowMs() }

// SystemClock returns a Clock backed by the wall clock (domain.NowMs).
func SystemClock() Clock { return systemClock{} }
