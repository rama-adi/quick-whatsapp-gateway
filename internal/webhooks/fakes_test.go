package webhooks

import (
	"context"
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// fixedClock is a deterministic Clock for tests.
type fixedClock struct{ ms int64 }

func (c *fixedClock) NowMs() int64 { return c.ms }

// fakeWebhookRepo records ListMatching/Get calls and returns canned data.
type fakeWebhookRepo struct {
	matching map[string][]domain.Webhook // keyed by eventType
	byID     map[string]domain.Webhook
	getErr   error

	lastOrganization, lastSession, lastType string
	listErr                                 error
}

func (r *fakeWebhookRepo) ListMatching(_ context.Context, organization, session, eventType string) ([]domain.Webhook, error) {
	r.lastOrganization, r.lastSession, r.lastType = organization, session, eventType
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.matching[eventType], nil
}

func (r *fakeWebhookRepo) Get(_ context.Context, id string) (domain.Webhook, error) {
	if r.getErr != nil {
		return domain.Webhook{}, r.getErr
	}
	h, ok := r.byID[id]
	if !ok {
		return domain.Webhook{}, domain.ErrNotFound("webhook not found")
	}
	return h, nil
}

// fakeEventStore returns a canned event.
type fakeEventStore struct {
	events map[string]domain.Event
	getErr error
}

func (s *fakeEventStore) GetEvent(_ context.Context, eventID string) (domain.Event, error) {
	if s.getErr != nil {
		return domain.Event{}, s.getErr
	}
	e, ok := s.events[eventID]
	if !ok {
		return domain.Event{}, domain.ErrNotFound("event not found")
	}
	return e, nil
}

// markCall captures one terminal bookkeeping transition for assertions.
type markCall struct {
	kind         string // "delivered" | "failed" | "dead"
	id           uint64
	attempts     int
	nextRetryAt  int64
	responseCode *int
	lastErr      string
}

// fakeDeliveryRepo records every transition and serves ClaimDue/ExistsTerminal
// from in-memory slices/maps.
type fakeDeliveryRepo struct {
	created   []domain.WebhookDelivery
	due       []domain.WebhookDelivery
	terminal  map[string]bool // key: webhookID+"|"+eventID
	calls     []markCall
	createErr error
	claimErr  error
	existsErr error
}

func deliveryKey(webhookID, eventID string) string { return webhookID + "|" + eventID }

func (r *fakeDeliveryRepo) Create(_ context.Context, d *domain.WebhookDelivery) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.created = append(r.created, *d)
	return nil
}

func (r *fakeDeliveryRepo) ClaimDue(_ context.Context, _ int64, limit int) ([]domain.WebhookDelivery, error) {
	if r.claimErr != nil {
		return nil, r.claimErr
	}
	if limit < len(r.due) {
		return r.due[:limit], nil
	}
	return r.due, nil
}

func (r *fakeDeliveryRepo) MarkDelivered(_ context.Context, id uint64, attempts, responseCode int) error {
	rc := responseCode
	r.calls = append(r.calls, markCall{kind: "delivered", id: id, attempts: attempts, responseCode: &rc})
	return nil
}

func (r *fakeDeliveryRepo) MarkFailed(_ context.Context, id uint64, attempts int, nextRetryAt int64, responseCode *int, lastErr string) error {
	r.calls = append(r.calls, markCall{kind: "failed", id: id, attempts: attempts, nextRetryAt: nextRetryAt, responseCode: responseCode, lastErr: lastErr})
	return nil
}

func (r *fakeDeliveryRepo) MarkDead(_ context.Context, id uint64, attempts int, responseCode *int, lastErr string) error {
	r.calls = append(r.calls, markCall{kind: "dead", id: id, attempts: attempts, responseCode: responseCode, lastErr: lastErr})
	return nil
}

func (r *fakeDeliveryRepo) ExistsTerminal(_ context.Context, webhookID, eventID string) (bool, error) {
	if r.existsErr != nil {
		return false, r.existsErr
	}
	return r.terminal[deliveryKey(webhookID, eventID)], nil
}

// fakeHTTPDoer returns a canned response or error, and captures the request.
type fakeHTTPDoer struct {
	resp    *http.Response
	err     error
	gotReq  *http.Request
	gotBody []byte
}

func (h *fakeHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	h.gotReq = req
	if req.Body != nil {
		b := make([]byte, 0, 256)
		buf := make([]byte, 256)
		for {
			n, err := req.Body.Read(buf)
			b = append(b, buf[:n]...)
			if err != nil {
				break
			}
		}
		h.gotBody = b
	}
	if h.err != nil {
		return nil, h.err
	}
	return h.resp, nil
}

// staticDecryptor returns its plaintext verbatim (identity) so tests can supply
// the "decrypted" secret directly as the stored bytes.
type staticDecryptor struct {
	plaintext []byte
	err       error
}

func (d *staticDecryptor) Decrypt(_ []byte) ([]byte, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.plaintext, nil
}
