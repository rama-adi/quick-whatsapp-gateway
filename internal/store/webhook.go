package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// WebhookRepo is the repository for webhooks (§5). HMAC secrets are stored
// AES-GCM encrypted by the caller; this repo only persists the opaque bytes.
type WebhookRepo struct {
	q *storedb.Queries
}

// NewWebhookRepo constructs a WebhookRepo.
func NewWebhookRepo(db storedb.DBTX) *WebhookRepo { return &WebhookRepo{q: storedb.New(db)} }

func webhookFromRow(row storedb.Webhook) (domain.Webhook, error) {
	w := domain.Webhook{
		ID:             row.ID,
		OrganizationID: row.OrganizationID,
		SessionID:      stringPtrFromNull(row.SessionID),
		URL:            row.Url,
		HMACSecret:     append([]byte(nil), row.HmacSecret...),
		Active:         row.Active,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	if len(row.Events) > 0 {
		if err := json.Unmarshal(row.Events, &w.Events); err != nil {
			return domain.Webhook{}, scanErr("webhooks.events", err)
		}
	}
	if len(row.CustomHeaders) > 0 {
		if err := json.Unmarshal(row.CustomHeaders, &w.CustomHeaders); err != nil {
			return domain.Webhook{}, scanErr("webhooks.custom_headers", err)
		}
	}
	if len(row.RetryPolicy) > 0 {
		if err := json.Unmarshal(row.RetryPolicy, &w.RetryPolicy); err != nil {
			return domain.Webhook{}, scanErr("webhooks.retry_policy", err)
		}
	}
	return w, nil
}

// webhookJSON marshals the JSON columns once for insert/update.
func webhookJSON(w domain.Webhook) (events, customHeaders, retryPolicy []byte, err error) {
	if events, err = json.Marshal(w.Events); err != nil {
		return nil, nil, nil, fmt.Errorf("store: marshal events: %w", err)
	}
	// custom_headers is nullable; nil map -> SQL NULL by leaving []byte nil.
	if w.CustomHeaders != nil {
		if customHeaders, err = json.Marshal(w.CustomHeaders); err != nil {
			return nil, nil, nil, fmt.Errorf("store: marshal custom_headers: %w", err)
		}
	}
	if retryPolicy, err = json.Marshal(w.RetryPolicy); err != nil {
		return nil, nil, nil, fmt.Errorf("store: marshal retry_policy: %w", err)
	}
	return events, customHeaders, retryPolicy, nil
}

// Create inserts a webhook.
func (r *WebhookRepo) Create(ctx context.Context, w domain.Webhook) error {
	events, customHeaders, retryPolicy, err := webhookJSON(w)
	if err != nil {
		return err
	}
	err = r.q.CreateWebhook(ctx, storedb.CreateWebhookParams{
		ID:             w.ID,
		OrganizationID: w.OrganizationID,
		SessionID:      nullString(w.SessionID),
		Url:            w.URL,
		Events:         events,
		HmacSecret:     w.HMACSecret,
		CustomHeaders:  nullableJSON(customHeaders),
		RetryPolicy:    retryPolicy,
		Active:         w.Active,
		CreatedAt:      w.CreatedAt,
		UpdatedAt:      w.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("store: create webhook: %w", err)
	}
	return nil
}

// Get fetches a webhook by id. Maps no-rows to not_found.
func (r *WebhookRepo) Get(ctx context.Context, id string) (domain.Webhook, error) {
	row, err := r.q.GetWebhook(ctx, storedb.GetWebhookParams{ID: id})
	if err != nil {
		return domain.Webhook{}, notFound(err, "webhook")
	}
	return webhookFromRow(row)
}

// ListByOrg returns all webhooks for a organization ordered by created_at desc.
func (r *WebhookRepo) ListByOrg(ctx context.Context, organizationID string) ([]domain.Webhook, error) {
	rows, err := r.q.ListWebhooksByOrg(ctx, storedb.ListWebhooksByOrgParams{OrganizationID: organizationID})
	if err != nil {
		return nil, fmt.Errorf("store: list webhooks: %w", err)
	}
	out := make([]domain.Webhook, 0, len(rows))
	for _, row := range rows {
		w, err := webhookFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// ListActiveForEvent returns active webhooks for a organization whose session scope
// matches (session_id IS NULL = all organization sessions, or equals sessionID). The
// dispatcher filters the `events` JSON in-process (it may contain "*"), so this
// returns the candidate set rather than doing JSON matching in SQL.
func (r *WebhookRepo) ListActiveForEvent(ctx context.Context, organizationID, sessionID string) ([]domain.Webhook, error) {
	rows, err := r.q.ListActiveWebhooksForEvent(ctx, storedb.ListActiveWebhooksForEventParams{
		OrganizationID: organizationID,
		SessionID:      sqlString(sessionID),
	})
	if err != nil {
		return nil, fmt.Errorf("store: list active webhooks: %w", err)
	}
	out := make([]domain.Webhook, 0, len(rows))
	for _, row := range rows {
		w, err := webhookFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// Update writes the mutable fields of a webhook, keyed on id.
func (r *WebhookRepo) Update(ctx context.Context, w domain.Webhook) error {
	events, customHeaders, retryPolicy, err := webhookJSON(w)
	if err != nil {
		return err
	}
	n, err := r.q.UpdateWebhook(ctx, storedb.UpdateWebhookParams{
		SessionID:     nullString(w.SessionID),
		Url:           w.URL,
		Events:        events,
		HmacSecret:    w.HMACSecret,
		CustomHeaders: nullableJSON(customHeaders),
		RetryPolicy:   retryPolicy,
		Active:        w.Active,
		UpdatedAt:     w.UpdatedAt,
		ID:            w.ID,
	})
	if err != nil {
		return fmt.Errorf("store: update webhook: %w", err)
	}
	return rowsAffectedOrNotFound(n, "webhook")
}

// Delete removes a webhook by id.
func (r *WebhookRepo) Delete(ctx context.Context, id string) error {
	n, err := r.q.DeleteWebhook(ctx, storedb.DeleteWebhookParams{ID: id})
	if err != nil {
		return fmt.Errorf("store: delete webhook: %w", err)
	}
	return rowsAffectedOrNotFound(n, "webhook")
}
