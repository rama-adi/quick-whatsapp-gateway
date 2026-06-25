package service

import (
	"context"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// ---------------------------------------------------------------------------
// ChannelService — channels/newsletters (§11 Channels).
// ---------------------------------------------------------------------------

// ChannelService backs the channel endpoints over the live ChannelOps surface.
// Channel message history (§11 GET /channels/{jid}/messages) is read from the
// stored messages by chat_jid.
type ChannelService struct {
	store *store.Store
	ops   ChannelOps
	log   *slog.Logger
}

// NewChannelService constructs a ChannelService.
func NewChannelService(s *store.Store, ops ChannelOps, log *slog.Logger) *ChannelService {
	if log == nil {
		log = slog.Default()
	}
	return &ChannelService{store: s, ops: ops, log: log}
}

func (s *ChannelService) requireSession(ctx context.Context, tenantID, sessionID string) error {
	sess, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.TenantID != tenantID {
		return domain.ErrNotFound("session not found")
	}
	return nil
}

func (s *ChannelService) live(ctx context.Context, tenantID, sessionID string) error {
	if err := s.requireSession(ctx, tenantID, sessionID); err != nil {
		return err
	}
	if s.ops == nil {
		return errLiveUnavailable()
	}
	return nil
}

// Create creates a channel/newsletter (§11 POST /channels).
func (s *ChannelService) Create(ctx context.Context, tenantID, sessionID, name, description string) (string, error) {
	if name == "" {
		return "", domain.ErrValidation("name is required")
	}
	if err := s.live(ctx, tenantID, sessionID); err != nil {
		return "", err
	}
	return s.ops.Create(ctx, sessionID, name, description)
}

// Follow follows a channel (§11 POST /channels/{jid}:follow).
func (s *ChannelService) Follow(ctx context.Context, tenantID, sessionID, jid string) error {
	if err := s.live(ctx, tenantID, sessionID); err != nil {
		return err
	}
	return s.ops.Follow(ctx, sessionID, jid)
}

// Unfollow unfollows a channel (§11 POST /channels/{jid}:unfollow).
func (s *ChannelService) Unfollow(ctx context.Context, tenantID, sessionID, jid string) error {
	if err := s.live(ctx, tenantID, sessionID); err != nil {
		return err
	}
	return s.ops.Unfollow(ctx, sessionID, jid)
}

// Mute mutes or unmutes a channel (§11 POST /channels/{jid}:mute).
func (s *ChannelService) Mute(ctx context.Context, tenantID, sessionID, jid string, mute bool) error {
	if err := s.live(ctx, tenantID, sessionID); err != nil {
		return err
	}
	return s.ops.Mute(ctx, sessionID, jid, mute)
}

// Messages returns stored channel messages (§11 GET /channels/{jid}/messages).
func (s *ChannelService) Messages(ctx context.Context, tenantID, sessionID, jid, cursor string, limit int) (store.Page[domain.Message], error) {
	if err := s.requireSession(ctx, tenantID, sessionID); err != nil {
		return store.Page[domain.Message]{}, err
	}
	return s.store.Messages.ListByChat(ctx, sessionID, jid, cursor, limit)
}

// ---------------------------------------------------------------------------
// StatusService — status/stories (§11 Status).
// ---------------------------------------------------------------------------

// StatusService backs the status (stories) endpoints.
type StatusService struct {
	store  *store.Store
	poster StatusPoster
	log    *slog.Logger
}

// NewStatusService constructs a StatusService.
func NewStatusService(s *store.Store, poster StatusPoster, log *slog.Logger) *StatusService {
	if log == nil {
		log = slog.Default()
	}
	return &StatusService{store: s, poster: poster, log: log}
}

func (s *StatusService) requireSession(ctx context.Context, tenantID, sessionID string) error {
	sess, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.TenantID != tenantID {
		return domain.ErrNotFound("session not found")
	}
	return nil
}

// StatusKind discriminates the §11 POST /status body. Only "text" is supported in
// v1; media kinds return not_implemented consistently with the media send types.
const (
	StatusKindText  = "text"
	StatusKindImage = "image"
)

// PostText posts a text status (§11 POST /status).
func (s *StatusService) PostText(ctx context.Context, tenantID, sessionID, text string) (string, error) {
	if err := s.requireSession(ctx, tenantID, sessionID); err != nil {
		return "", err
	}
	if text == "" {
		return "", domain.ErrValidation("text is required")
	}
	if s.poster == nil {
		return "", errLiveUnavailable()
	}
	return s.poster.PostText(ctx, sessionID, text)
}

// PostImage is the image-status path — not implemented in v1 (§11: image => 501).
func (s *StatusService) PostImage(ctx context.Context, tenantID, sessionID string) (string, error) {
	if err := s.requireSession(ctx, tenantID, sessionID); err != nil {
		return "", err
	}
	return "", domain.ErrNotImplemented("image status is not implemented yet")
}

// ---------------------------------------------------------------------------
// PresenceService — account-wide presence (§11 Presence).
// ---------------------------------------------------------------------------

// PresenceService backs the presence endpoint.
type PresenceService struct {
	store   *store.Store
	control PresenceController
	log     *slog.Logger
}

// NewPresenceService constructs a PresenceService.
func NewPresenceService(s *store.Store, control PresenceController, log *slog.Logger) *PresenceService {
	if log == nil {
		log = slog.Default()
	}
	return &PresenceService{store: s, control: control, log: log}
}

func (s *PresenceService) requireSession(ctx context.Context, tenantID, sessionID string) error {
	sess, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.TenantID != tenantID {
		return domain.ErrNotFound("session not found")
	}
	return nil
}

// Set sets account-wide presence: "online" or "offline" (§11 PUT /presence).
func (s *PresenceService) Set(ctx context.Context, tenantID, sessionID, state string) error {
	if err := s.requireSession(ctx, tenantID, sessionID); err != nil {
		return err
	}
	if state != "online" && state != "offline" {
		return domain.ErrValidation("state must be online or offline")
	}
	if s.control == nil {
		return errLiveUnavailable()
	}
	return s.control.SetPresence(ctx, sessionID, state)
}

// ---------------------------------------------------------------------------
// AdminService — cross-tenant oversight (§11 Admin).
// ---------------------------------------------------------------------------

// AdminService backs the super-admin oversight endpoints.
type AdminService struct {
	store *store.Store
	log   *slog.Logger
}

// NewAdminService constructs an AdminService.
func NewAdminService(s *store.Store, log *slog.Logger) *AdminService {
	if log == nil {
		log = slog.Default()
	}
	return &AdminService{store: s, log: log}
}

// ListAllSessions returns every session across all tenants (super_admin).
func (s *AdminService) ListAllSessions(ctx context.Context) ([]domain.WASession, error) {
	return s.store.Sessions.ListAll(ctx)
}
