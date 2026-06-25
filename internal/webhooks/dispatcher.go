package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// maxResponseBodyRead bounds how much of an error response we read into
// last_error so a hostile endpoint can't make us buffer megabytes.
const maxResponseBodyRead = 4 << 10 // 4 KiB

// DefaultClaimLimit is how many due deliveries DeliverDue claims per pass when
// the caller does not specify a limit.
const DefaultClaimLimit = 50

// Dispatcher claims due webhook deliveries and POSTs the event body to each
// endpoint, applying HMAC signing, retry backoff, dedup and dead-lettering. The
// claim/loop cadence is driven externally (an injected scheduler or asynq, later);
// this type exposes DeliverDue (one claim+send pass) and Deliver (a single
// delivery) so both are independently testable.
type Dispatcher struct {
	webhooks   WebhookRepo
	deliveries WebhookDeliveryRepo
	events     EventStore
	http       HTTPDoer
	decryptor  Decryptor
	clock      Clock
	log        *slog.Logger
}

// NewDispatcher builds a Dispatcher. clock and log may be nil (a system clock and
// the slog default are used). decryptor may be nil only if no webhook ever sets
// an hmac secret; when a secret is present and decryptor is nil the send fails
// (and retries), which surfaces the misconfiguration loudly.
func NewDispatcher(
	webhooks WebhookRepo,
	deliveries WebhookDeliveryRepo,
	events EventStore,
	httpDoer HTTPDoer,
	decryptor Decryptor,
	clock Clock,
	log *slog.Logger,
) *Dispatcher {
	if clock == nil {
		clock = SystemClock()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{
		webhooks:   webhooks,
		deliveries: deliveries,
		events:     events,
		http:       httpDoer,
		decryptor:  decryptor,
		clock:      clock,
		log:        log,
	}
}

// DeliverDue claims up to limit due deliveries and attempts each one. It returns
// the number of deliveries processed (attempted). limit <= 0 uses
// DefaultClaimLimit. A claim failure is returned; individual delivery failures
// are recorded on the row (and logged) but do not abort the pass.
func (d *Dispatcher) DeliverDue(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = DefaultClaimLimit
	}
	now := d.clock.NowMs()
	due, err := d.deliveries.ClaimDue(ctx, now, limit)
	if err != nil {
		return 0, fmt.Errorf("claim due deliveries: %w", err)
	}
	for i := range due {
		if err := d.Deliver(ctx, due[i]); err != nil {
			// Deliver only returns an error for an unrecoverable bookkeeping
			// failure (the repo write itself failed); the delivery state could
			// not be advanced, so log and move on — ClaimDue will surface it
			// again on the next pass.
			d.log.ErrorContext(ctx, "webhook delivery bookkeeping failed",
				"delivery_id", due[i].ID, "webhook_id", due[i].WebhookID, "err", err)
		}
	}
	return len(due), nil
}

// Deliver attempts a single claimed delivery: load the webhook + event, build
// and sign the request, POST it, then record the outcome (delivered / failed
// +reschedule / dead). The returned error is non-nil only when recording the
// outcome failed (i.e. the delivery state is now uncertain); a failed POST is a
// normal, non-error result captured on the row.
func (d *Dispatcher) Deliver(ctx context.Context, del domain.WebhookDelivery) error {
	attempts := del.Attempts + 1 // this is attempt number `attempts`

	hook, err := d.webhooks.Get(ctx, del.WebhookID)
	if err != nil {
		// Webhook deleted out from under a pending delivery: it can never
		// succeed, so dead-letter it immediately rather than retry forever.
		if isNotFound(err) {
			return d.deliveries.MarkDead(ctx, del.ID, attempts, nil,
				fmt.Sprintf("webhook %s not found", del.WebhookID))
		}
		return d.fail(ctx, del, hook, attempts, nil, fmt.Sprintf("load webhook: %v", err))
	}

	evt, err := d.events.GetEvent(ctx, del.EventID)
	if err != nil {
		if isNotFound(err) {
			return d.deliveries.MarkDead(ctx, del.ID, attempts, nil,
				fmt.Sprintf("event %s not found", del.EventID))
		}
		return d.fail(ctx, del, hook, attempts, nil, fmt.Sprintf("load event: %v", err))
	}

	body, err := json.Marshal(evt)
	if err != nil {
		// A non-serializable payload will never serialize — dead-letter it.
		return d.deliveries.MarkDead(ctx, del.ID, attempts, nil,
			fmt.Sprintf("marshal event: %v", err))
	}

	req, err := d.buildRequest(ctx, hook, evt, body)
	if err != nil {
		return d.fail(ctx, del, hook, attempts, nil, err.Error())
	}

	resp, err := d.http.Do(req)
	if err != nil {
		return d.fail(ctx, del, hook, attempts, nil, fmt.Sprintf("http do: %v", err))
	}
	code := resp.StatusCode
	defer resp.Body.Close()

	if code >= 200 && code < 300 {
		// Drain (bounded) so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBodyRead))
		return d.deliveries.MarkDelivered(ctx, del.ID, attempts, code)
	}

	// Non-2xx: capture a bounded slice of the body for diagnostics.
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyRead))
	msg := fmt.Sprintf("non-2xx status %d: %s", code, bytes.TrimSpace(snippet))
	return d.fail(ctx, del, hook, attempts, &code, msg)
}

// fail records a failed attempt: if the attempt budget is exhausted, dead-letter;
// otherwise schedule the next retry per the webhook's RetryPolicy.
func (d *Dispatcher) fail(
	ctx context.Context,
	del domain.WebhookDelivery,
	hook domain.Webhook,
	attempts int,
	responseCode *int,
	lastErr string,
) error {
	if attempts >= maxAttempts(hook.RetryPolicy) {
		return d.deliveries.MarkDead(ctx, del.ID, attempts, responseCode, lastErr)
	}
	// attempts is 1-based and is the number of the attempt that just failed;
	// the delay before the next attempt uses that same index so the first retry
	// waits delaySeconds, the second 2*delaySeconds, etc.
	delay := backoffSeconds(hook.RetryPolicy.Policy, hook.RetryPolicy.DelaySeconds, attempts)
	next := d.clock.NowMs() + delay*1000
	return d.deliveries.MarkFailed(ctx, del.ID, attempts, next, responseCode, lastErr)
}

// buildRequest constructs the signed POST for a delivery.
func (d *Dispatcher) buildRequest(ctx context.Context, hook domain.Webhook, evt domain.Event, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Reuse the event id as the request id so a redelivery of the same event is
	// idempotent/correlatable on the consumer side.
	req.Header.Set(HeaderRequestID, evt.ID)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(d.clock.NowMs(), 10))

	if len(hook.HMACSecret) > 0 {
		if d.decryptor == nil {
			return nil, fmt.Errorf("webhook %s has hmac secret but no decryptor configured", hook.ID)
		}
		secret, err := d.decryptor.Decrypt(hook.HMACSecret)
		if err != nil {
			return nil, fmt.Errorf("decrypt hmac secret: %w", err)
		}
		req.Header.Set(HeaderHMAC, SignHMAC(secret, body))
		req.Header.Set(HeaderHMACAlgorithm, HMACAlgorithm)
	}

	// Custom headers last so they can override defaults if intentionally set
	// (e.g. a fixed Content-Type), per the webhook owner's configuration.
	for k, v := range hook.CustomHeaders {
		req.Header.Set(k, v)
	}
	return req, nil
}

// isNotFound recognizes the domain API "not_found" error in addition to the
// sentinel, so a repo returning either form lets us dead-letter cleanly.
func isNotFound(err error) bool {
	var apiErr *domain.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == domain.CodeNotFound
	}
	return false
}
