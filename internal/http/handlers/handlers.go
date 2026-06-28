// Package handlers holds thin HTTP handlers (validate -> service -> encode) for
// every REST resource (§11). Handlers depend on small CONSUMER interfaces (one
// per service) rather than the concrete service structs, so they can be unit
// tested with fakes; the concrete services from internal/service satisfy them.
package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// ---------------------------------------------------------------------------
// Consumer interfaces (satisfied by the concrete *service.* structs).
// ---------------------------------------------------------------------------

// SessionSvc is the session lifecycle surface the handlers call.
type SessionSvc interface {
	Create(ctx context.Context, organizationID string, in service.CreateInput) (domain.WASession, error)
	List(ctx context.Context, organizationID string) ([]domain.WASession, error)
	Get(ctx context.Context, organizationID, id string) (domain.WASession, error)
	Start(ctx context.Context, organizationID, id string) error
	Stop(ctx context.Context, organizationID, id string) error
	Restart(ctx context.Context, organizationID, id string) error
	Logout(ctx context.Context, organizationID, id string) error
	Delete(ctx context.Context, organizationID, id string) error
	Me(ctx context.Context, organizationID, id string) (service.Me, error)
	QR(ctx context.Context, organizationID, id string) (service.QR, error)
	PairingCode(ctx context.Context, organizationID, id, phone string) (string, error)
}

// MessageSvc is the outbound send + message-op surface.
type MessageSvc interface {
	Send(ctx context.Context, organizationID, sessionID string, req domain.SendRequest, opts outbound.SendOptions) (outbound.SendResult, error)
	Edit(ctx context.Context, organizationID, sessionID, chat, msgID, newText string) (outbound.SendResult, error)
	Revoke(ctx context.Context, organizationID, sessionID, chat, sender, msgID string) (outbound.SendResult, error)
	React(ctx context.Context, organizationID, sessionID, chat, sender, msgID, emoji string) (outbound.SendResult, error)
	Forward(ctx context.Context, organizationID, sessionID, chat, sender, msgID, to string) (outbound.SendResult, error)
	Vote(ctx context.Context, organizationID, sessionID, chat, sender, msgID string, options []string) (outbound.SendResult, error)
}

// WebhookSvc is the webhook CRUD surface.
type WebhookSvc interface {
	Create(ctx context.Context, organizationID string, in service.WebhookInput) (domain.Webhook, error)
	List(ctx context.Context, organizationID string) ([]domain.Webhook, error)
	Get(ctx context.Context, organizationID, id string) (domain.Webhook, error)
	Update(ctx context.Context, organizationID, id string, in service.WebhookInput) (domain.Webhook, error)
	Delete(ctx context.Context, organizationID, id string) error
}

// AdminSvc is the cross-organization oversight surface.
type AdminSvc interface {
	ListAllSessions(ctx context.Context) ([]domain.WASession, error)
	StartBackfill(ctx context.Context, sessionID string) (domain.BackfillJob, error)
	BackfillStatus(ctx context.Context, sessionID string) (domain.BackfillJob, error)
}

// BackupSvc is the user-facing WhatsApp backup (crypt15) import surface.
type BackupSvc interface {
	StartImport(ctx context.Context, organizationID, sessionID string, isSuperAdmin bool, ciphertext []byte, key string) (domain.BackfillImport, error)
	ImportStatus(ctx context.Context, organizationID, sessionID string, isSuperAdmin bool) (domain.BackfillImport, error)
}

// ChatSvc is the chat viewer + read-state surface (§11 Chats).
type ChatSvc interface {
	List(ctx context.Context, organizationID, sessionID, cursor string, limit int) (store.Page[domain.Chat], error)
	Get(ctx context.Context, organizationID, sessionID, chatJID string) (domain.Chat, error)
	ListMessages(ctx context.Context, organizationID, sessionID, chatJID, cursor string, limit int) (store.Page[domain.Message], error)
	Read(ctx context.Context, organizationID, sessionID, chatJID string) (domain.Chat, error)
	Update(ctx context.Context, organizationID, sessionID, chatJID string, in service.ChatUpdate) (domain.Chat, error)
	Delete(ctx context.Context, organizationID, sessionID, chatJID string) error
	SetPresence(ctx context.Context, organizationID, sessionID, chatJID, state string) error
}

// ContactSvc is the "found users" + live contact surface (§11 Contacts).
type ContactSvc interface {
	List(ctx context.Context, organizationID, sessionID string, f store.ContactFilter, cursor string, limit int) (store.Page[domain.Contact], error)
	Get(ctx context.Context, organizationID, sessionID, lid string) (service.ContactDetail, error)
	Check(ctx context.Context, organizationID, sessionID, phone string) (domain.OnWhatsApp, error)
	Picture(ctx context.Context, organizationID, sessionID, jid string) (domain.ProfilePicture, error)
	About(ctx context.Context, organizationID, sessionID, jid string) (string, error)
	SetBlocked(ctx context.Context, organizationID, sessionID, jid string, blocked bool) error
}

// GroupSvc is the group-management surface (§11 Groups).
type GroupSvc interface {
	Create(ctx context.Context, organizationID, sessionID, name string, participants []string) (domain.GroupInfo, error)
	List(ctx context.Context, organizationID, sessionID string) ([]domain.Group, error)
	Get(ctx context.Context, organizationID, sessionID, groupJID string) (domain.Group, error)
	Members(ctx context.Context, organizationID, sessionID, groupJID string) ([]domain.GroupMember, error)
	AddMembers(ctx context.Context, organizationID, sessionID, groupJID string, jids []string) error
	RemoveMember(ctx context.Context, organizationID, sessionID, groupJID, jid string) error
	Promote(ctx context.Context, organizationID, sessionID, groupJID, jid string) error
	Demote(ctx context.Context, organizationID, sessionID, groupJID, jid string) error
	UpdateSettings(ctx context.Context, organizationID, sessionID, groupJID string, in domain.GroupSettings) error
	InviteLink(ctx context.Context, organizationID, sessionID, groupJID string) (string, error)
	RevokeInvite(ctx context.Context, organizationID, sessionID, groupJID string) (string, error)
	Join(ctx context.Context, organizationID, sessionID, invite string) (string, error)
	Leave(ctx context.Context, organizationID, sessionID, groupJID string) error
	ApproveMembers(ctx context.Context, organizationID, sessionID, groupJID string, jids []string) error
}

// ChannelSvc is the channel/newsletter surface (§11 Channels).
type ChannelSvc interface {
	Create(ctx context.Context, organizationID, sessionID, name, description string) (string, error)
	Follow(ctx context.Context, organizationID, sessionID, jid string) error
	Unfollow(ctx context.Context, organizationID, sessionID, jid string) error
	Mute(ctx context.Context, organizationID, sessionID, jid string, mute bool) error
	Messages(ctx context.Context, organizationID, sessionID, jid, cursor string, limit int) (store.Page[domain.Message], error)
}

// StatusSvc is the status/stories surface (§11 Status).
type StatusSvc interface {
	PostText(ctx context.Context, organizationID, sessionID, text string) (string, error)
	PostImage(ctx context.Context, organizationID, sessionID string) (string, error)
}

// PresenceSvc is the account-wide presence surface (§11 Presence).
type PresenceSvc interface {
	Set(ctx context.Context, organizationID, sessionID, state string) error
}

// Compile-time proof that the concrete services satisfy the handler interfaces.
var (
	_ SessionSvc  = (*service.SessionService)(nil)
	_ MessageSvc  = (*service.MessageService)(nil)
	_ WebhookSvc  = (*service.WebhookService)(nil)
	_ AdminSvc    = (*service.AdminService)(nil)
	_ BackupSvc   = (*service.BackupImportService)(nil)
	_ ChatSvc     = (*service.ChatService)(nil)
	_ ContactSvc  = (*service.ContactService)(nil)
	_ GroupSvc    = (*service.GroupService)(nil)
	_ ChannelSvc  = (*service.ChannelService)(nil)
	_ StatusSvc   = (*service.StatusService)(nil)
	_ PresenceSvc = (*service.PresenceService)(nil)
)

// Handlers bundles the service dependencies and exposes one http.HandlerFunc
// method per §11 endpoint. Realtime is no longer served here — the router owns
// the WebSocket transport; the gateway only publishes events to Redis.
type Handlers struct {
	Sessions SessionSvc
	Messages MessageSvc
	Webhooks WebhookSvc
	Admin    AdminSvc
	Backup   BackupSvc
	Chats    ChatSvc
	Contacts ContactSvc
	Groups   GroupSvc
	Channels ChannelSvc
	Status   StatusSvc
	Presence PresenceSvc

	Log *slog.Logger
}

// New builds Handlers from the concrete service aggregate. The concrete services
// satisfy the consumer interfaces above.
func New(s *service.Services, log *slog.Logger) *Handlers {
	if log == nil {
		log = slog.Default()
	}
	return &Handlers{
		Sessions: s.Sessions,
		Messages: s.Messages,
		Webhooks: s.Webhooks,
		Admin:    s.Admin,
		Backup:   s.Backup,
		Chats:    s.Chats,
		Contacts: s.Contacts,
		Groups:   s.Groups,
		Channels: s.Channels,
		Status:   s.Status,
		Presence: s.Presence,
		Log:      log,
	}
}

// ---------------------------------------------------------------------------
// Shared helpers.
// ---------------------------------------------------------------------------

// organization pulls the authenticated organization id from the request context (set by the
// API-key auth or cookie-session middleware). Returns false (and writes a 401)
// when absent.
func organization(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := httpx.OrganizationID(r.Context())
	if id == "" {
		httpx.WriteError(w, domain.ErrUnauthorized("authentication required"))
		return "", false
	}
	return id, true
}

// param reads a chi URL parameter, URL-decoding it. chi routes on the raw
// (escaped) path when URL.RawPath is set, so URLParam returns the still-encoded
// segment — e.g. a chat/contact JID arrives as "120363...%40g.us" rather than
// "120363...@g.us". WhatsApp identifiers carry "@" (and ":"), so without this the
// value never matches the stored jid and the lookup 404s. Falls back to the raw
// value if it isn't valid percent-encoding.
func param(r *http.Request, name string) string {
	raw := chi.URLParam(r, name)
	if decoded, err := url.PathUnescape(raw); err == nil {
		return decoded
	}
	return raw
}
