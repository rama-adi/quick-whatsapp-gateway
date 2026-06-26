package service

import (
	"context"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/crypto"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// WebhookService owns webhook CRUD (§9). The HMAC signing secret is AES-GCM
// encrypted at rest (never returned over the API); everything else round-trips
// from the webhooks table.
type WebhookService struct {
	repo         *store.WebhookRepo
	crypto       *crypto.AESGCM
	defaultDelay int
	defaultTries int
	log          *slog.Logger
}

// NewWebhookService constructs a WebhookService. defaultDelay/defaultTries seed
// a webhook's retry policy when the caller does not specify one.
func NewWebhookService(repo *store.WebhookRepo, c *crypto.AESGCM, defaultDelay, defaultTries int, log *slog.Logger) *WebhookService {
	if log == nil {
		log = slog.Default()
	}
	if defaultDelay <= 0 {
		defaultDelay = 2
	}
	if defaultTries <= 0 {
		defaultTries = 15
	}
	return &WebhookService{repo: repo, crypto: c, defaultDelay: defaultDelay, defaultTries: defaultTries, log: log}
}

// WebhookInput is the create/update body. Secret is the plaintext HMAC signing
// secret; it is encrypted before persistence and never read back.
type WebhookInput struct {
	SessionID     *string
	URL           string
	Events        []string
	Secret        *string
	CustomHeaders map[string]string
	RetryPolicy   *domain.RetryPolicy
	Active        *bool
}

func (s *WebhookService) defaultRetry() domain.RetryPolicy {
	return domain.RetryPolicy{Policy: "exponential", DelaySeconds: s.defaultDelay, Attempts: s.defaultTries}
}

// Create persists a new webhook.
func (s *WebhookService) Create(ctx context.Context, organizationID string, in WebhookInput) (domain.Webhook, error) {
	if in.URL == "" {
		return domain.Webhook{}, domain.ErrValidation("url is required")
	}
	if len(in.Events) == 0 {
		return domain.Webhook{}, domain.ErrValidation("events is required")
	}
	now := domain.NowMs()
	w := domain.Webhook{
		ID:             domain.NewWebhookID(),
		OrganizationID: organizationID,
		SessionID:      in.SessionID,
		URL:            in.URL,
		Events:         in.Events,
		CustomHeaders:  in.CustomHeaders,
		RetryPolicy:    s.defaultRetry(),
		Active:         true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if in.RetryPolicy != nil {
		w.RetryPolicy = *in.RetryPolicy
	}
	if in.Active != nil {
		w.Active = *in.Active
	}
	if in.Secret != nil && *in.Secret != "" {
		enc, err := s.encryptSecret(*in.Secret)
		if err != nil {
			return domain.Webhook{}, err
		}
		w.HMACSecret = enc
	}
	if err := s.repo.Create(ctx, w); err != nil {
		return domain.Webhook{}, err
	}
	return w, nil
}

// List returns the organization's webhooks.
func (s *WebhookService) List(ctx context.Context, organizationID string) ([]domain.Webhook, error) {
	return s.repo.ListByOrg(ctx, organizationID)
}

// Get loads one webhook, enforcing organization ownership.
func (s *WebhookService) Get(ctx context.Context, organizationID, id string) (domain.Webhook, error) {
	w, err := s.repo.Get(ctx, id)
	if err != nil {
		return domain.Webhook{}, err
	}
	if w.OrganizationID != organizationID {
		return domain.Webhook{}, domain.ErrNotFound("webhook not found")
	}
	return w, nil
}

// Update applies a partial update to a webhook.
func (s *WebhookService) Update(ctx context.Context, organizationID, id string, in WebhookInput) (domain.Webhook, error) {
	w, err := s.Get(ctx, organizationID, id)
	if err != nil {
		return domain.Webhook{}, err
	}
	if in.URL != "" {
		w.URL = in.URL
	}
	if in.Events != nil {
		w.Events = in.Events
	}
	if in.CustomHeaders != nil {
		w.CustomHeaders = in.CustomHeaders
	}
	if in.RetryPolicy != nil {
		w.RetryPolicy = *in.RetryPolicy
	}
	if in.Active != nil {
		w.Active = *in.Active
	}
	w.SessionID = in.SessionID
	if in.Secret != nil {
		if *in.Secret == "" {
			w.HMACSecret = nil
		} else {
			enc, err := s.encryptSecret(*in.Secret)
			if err != nil {
				return domain.Webhook{}, err
			}
			w.HMACSecret = enc
		}
	}
	w.UpdatedAt = domain.NowMs()
	if err := s.repo.Update(ctx, w); err != nil {
		return domain.Webhook{}, err
	}
	return w, nil
}

// Delete removes a webhook.
func (s *WebhookService) Delete(ctx context.Context, organizationID, id string) error {
	if _, err := s.Get(ctx, organizationID, id); err != nil {
		return err
	}
	return s.repo.Delete(ctx, id)
}

func (s *WebhookService) encryptSecret(plaintext string) ([]byte, error) {
	if s.crypto == nil {
		return nil, domain.ErrInternal("encryption is not configured")
	}
	enc, err := s.crypto.Encrypt([]byte(plaintext))
	if err != nil {
		return nil, domain.ErrInternal("failed to encrypt secret")
	}
	return enc, nil
}
