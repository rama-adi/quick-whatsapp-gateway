package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// WebhookRepo is the repository for webhooks (§5). HMAC secrets are stored
// AES-GCM encrypted by the caller; this repo only persists the opaque bytes.
type WebhookRepo struct {
	db dbExecQuerier
}

// NewWebhookRepo constructs a WebhookRepo.
func NewWebhookRepo(db dbExecQuerier) *WebhookRepo { return &WebhookRepo{db: db} }

const webhookCols = `id, organization_id, session_id, url, events, hmac_secret,
	custom_headers, retry_policy, active, created_at, updated_at`

func scanWebhook(s rowScanner) (domain.Webhook, error) {
	var (
		w            domain.Webhook
		events       []byte
		customHeader []byte
		retryPolicy  []byte
	)
	err := s.Scan(
		&w.ID, &w.OrganizationID, &w.SessionID, &w.URL, &events, &w.HMACSecret,
		&customHeader, &retryPolicy, &w.Active, &w.CreatedAt, &w.UpdatedAt,
	)
	if err != nil {
		return domain.Webhook{}, err
	}
	if len(events) > 0 {
		if err := json.Unmarshal(events, &w.Events); err != nil {
			return domain.Webhook{}, scanErr("webhooks.events", err)
		}
	}
	if len(customHeader) > 0 {
		if err := json.Unmarshal(customHeader, &w.CustomHeaders); err != nil {
			return domain.Webhook{}, scanErr("webhooks.custom_headers", err)
		}
	}
	if len(retryPolicy) > 0 {
		if err := json.Unmarshal(retryPolicy, &w.RetryPolicy); err != nil {
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
	const q = `INSERT INTO webhooks
(id, organization_id, session_id, url, events, hmac_secret, custom_headers,
 retry_policy, active, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := r.db.ExecContext(ctx, q,
		w.ID, w.OrganizationID, w.SessionID, w.URL, events, w.HMACSecret, nullableJSON(customHeaders),
		retryPolicy, w.Active, w.CreatedAt, w.UpdatedAt,
	); err != nil {
		return fmt.Errorf("store: create webhook: %w", err)
	}
	return nil
}

// Get fetches a webhook by id. Maps no-rows to not_found.
func (r *WebhookRepo) Get(ctx context.Context, id string) (domain.Webhook, error) {
	q := "SELECT " + webhookCols + " FROM webhooks WHERE id = ?"
	w, err := scanWebhook(r.db.QueryRowContext(ctx, q, id))
	if err != nil {
		return domain.Webhook{}, notFound(err, "webhook")
	}
	return w, nil
}

// ListByOrg returns all webhooks for a organization ordered by created_at desc.
func (r *WebhookRepo) ListByOrg(ctx context.Context, organizationID string) ([]domain.Webhook, error) {
	q := "SELECT " + webhookCols + " FROM webhooks WHERE organization_id = ? ORDER BY created_at DESC"
	rows, err := r.db.QueryContext(ctx, q, organizationID)
	if err != nil {
		return nil, fmt.Errorf("store: list webhooks: %w", err)
	}
	defer rows.Close()
	var out []domain.Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ListActiveForEvent returns active webhooks for a organization whose session scope
// matches (session_id IS NULL = all organization sessions, or equals sessionID). The
// dispatcher filters the `events` JSON in-process (it may contain "*"), so this
// returns the candidate set rather than doing JSON matching in SQL.
func (r *WebhookRepo) ListActiveForEvent(ctx context.Context, organizationID, sessionID string) ([]domain.Webhook, error) {
	const q = `SELECT ` + webhookCols + ` FROM webhooks
WHERE organization_id = ? AND active = 1 AND (session_id IS NULL OR session_id = ?)
ORDER BY created_at DESC`
	rows, err := r.db.QueryContext(ctx, q, organizationID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: list active webhooks: %w", err)
	}
	defer rows.Close()
	var out []domain.Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Update writes the mutable fields of a webhook, keyed on id.
func (r *WebhookRepo) Update(ctx context.Context, w domain.Webhook) error {
	events, customHeaders, retryPolicy, err := webhookJSON(w)
	if err != nil {
		return err
	}
	const q = `UPDATE webhooks SET
		session_id=?, url=?, events=?, hmac_secret=?, custom_headers=?,
		retry_policy=?, active=?, updated_at=?
	WHERE id=?`
	res, err := r.db.ExecContext(ctx, q,
		w.SessionID, w.URL, events, w.HMACSecret, nullableJSON(customHeaders),
		retryPolicy, w.Active, w.UpdatedAt, w.ID,
	)
	if err != nil {
		return fmt.Errorf("store: update webhook: %w", err)
	}
	return affectedOrNotFound(res, "webhook")
}

// Delete removes a webhook by id.
func (r *WebhookRepo) Delete(ctx context.Context, id string) error {
	const q = "DELETE FROM webhooks WHERE id=?"
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("store: delete webhook: %w", err)
	}
	return affectedOrNotFound(res, "webhook")
}
