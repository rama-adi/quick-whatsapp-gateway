// Package handlers holds thin HTTP handlers (validate -> service -> encode) for
// every REST resource (§11). Handlers depend on small CONSUMER interfaces (one
// per service) rather than the concrete service structs, so they can be unit
// tested with fakes; the concrete services from internal/service satisfy them.
package handlers

import (
	"context"
	"log/slog"
	"net/http"

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
	Create(ctx context.Context, tenantID string, in service.CreateInput) (domain.WASession, error)
	List(ctx context.Context, tenantID string) ([]domain.WASession, error)
	Get(ctx context.Context, tenantID, id string) (domain.WASession, error)
	Start(ctx context.Context, tenantID, id string) error
	Stop(ctx context.Context, tenantID, id string) error
	Restart(ctx context.Context, tenantID, id string) error
	Logout(ctx context.Context, tenantID, id string) error
	Delete(ctx context.Context, tenantID, id string) error
	Me(ctx context.Context, tenantID, id string) (service.Me, error)
	QR(ctx context.Context, tenantID, id string) (service.QR, error)
	PairingCode(ctx context.Context, tenantID, id, phone string) (string, error)
}

// MessageSvc is the outbound send + message-op surface.
type MessageSvc interface {
	Send(ctx context.Context, tenantID, sessionID string, req domain.SendRequest, opts outbound.SendOptions) (outbound.SendResult, error)
	Edit(ctx context.Context, tenantID, sessionID, chat, msgID, newText string) (outbound.SendResult, error)
	Revoke(ctx context.Context, tenantID, sessionID, chat, sender, msgID string) (outbound.SendResult, error)
	React(ctx context.Context, tenantID, sessionID, chat, sender, msgID, emoji string) (outbound.SendResult, error)
	Forward(ctx context.Context, tenantID, sessionID, chat, sender, msgID, to string) (outbound.SendResult, error)
	Vote(ctx context.Context, tenantID, sessionID, chat, sender, msgID string, options []string) (outbound.SendResult, error)
}

// KeySvc is the API-key lifecycle surface.
type KeySvc interface {
	Create(ctx context.Context, tenantID string, in service.CreateKeyInput) (service.CreateKeyResult, error)
	List(ctx context.Context, tenantID string) ([]domain.APIKey, error)
	Get(ctx context.Context, tenantID, id string) (domain.APIKey, error)
	Delete(ctx context.Context, tenantID, id string) error
	Rotate(ctx context.Context, tenantID, id string) (service.CreateKeyResult, error)
}

// WebhookSvc is the webhook CRUD surface.
type WebhookSvc interface {
	Create(ctx context.Context, tenantID string, in service.WebhookInput) (domain.Webhook, error)
	List(ctx context.Context, tenantID string) ([]domain.Webhook, error)
	Get(ctx context.Context, tenantID, id string) (domain.Webhook, error)
	Update(ctx context.Context, tenantID, id string, in service.WebhookInput) (domain.Webhook, error)
	Delete(ctx context.Context, tenantID, id string) error
}

// AdminSvc is the cross-tenant oversight surface.
type AdminSvc interface {
	ListAllSessions(ctx context.Context) ([]domain.WASession, error)
}

// ChatSvc is the chat viewer + read-state surface (§11 Chats).
type ChatSvc interface {
	List(ctx context.Context, tenantID, sessionID, cursor string, limit int) (store.Page[domain.Chat], error)
	Get(ctx context.Context, tenantID, sessionID, chatJID string) (domain.Chat, error)
	ListMessages(ctx context.Context, tenantID, sessionID, chatJID, cursor string, limit int) (store.Page[domain.Message], error)
	Read(ctx context.Context, tenantID, sessionID, chatJID string) (domain.Chat, error)
	Update(ctx context.Context, tenantID, sessionID, chatJID string, in service.ChatUpdate) (domain.Chat, error)
	Delete(ctx context.Context, tenantID, sessionID, chatJID string) error
	SetPresence(ctx context.Context, tenantID, sessionID, chatJID, state string) error
}

// ContactSvc is the "found users" + live contact surface (§11 Contacts).
type ContactSvc interface {
	List(ctx context.Context, tenantID, sessionID string, f store.ContactFilter, cursor string, limit int) (store.Page[domain.Contact], error)
	Get(ctx context.Context, tenantID, sessionID, lid string) (service.ContactDetail, error)
	Check(ctx context.Context, tenantID, sessionID, phone string) (domain.OnWhatsApp, error)
	Picture(ctx context.Context, tenantID, sessionID, jid string) (domain.ProfilePicture, error)
	About(ctx context.Context, tenantID, sessionID, jid string) (string, error)
	SetBlocked(ctx context.Context, tenantID, sessionID, jid string, blocked bool) error
}

// GroupSvc is the group-management surface (§11 Groups).
type GroupSvc interface {
	Create(ctx context.Context, tenantID, sessionID, name string, participants []string) (domain.GroupInfo, error)
	List(ctx context.Context, tenantID, sessionID string) ([]domain.Group, error)
	Get(ctx context.Context, tenantID, sessionID, groupJID string) (domain.Group, error)
	Members(ctx context.Context, tenantID, sessionID, groupJID string) ([]domain.GroupMember, error)
	AddMembers(ctx context.Context, tenantID, sessionID, groupJID string, jids []string) error
	RemoveMember(ctx context.Context, tenantID, sessionID, groupJID, jid string) error
	Promote(ctx context.Context, tenantID, sessionID, groupJID, jid string) error
	Demote(ctx context.Context, tenantID, sessionID, groupJID, jid string) error
	UpdateSettings(ctx context.Context, tenantID, sessionID, groupJID string, in domain.GroupSettings) error
	InviteLink(ctx context.Context, tenantID, sessionID, groupJID string) (string, error)
	RevokeInvite(ctx context.Context, tenantID, sessionID, groupJID string) (string, error)
	Join(ctx context.Context, tenantID, sessionID, invite string) (string, error)
	Leave(ctx context.Context, tenantID, sessionID, groupJID string) error
	ApproveMembers(ctx context.Context, tenantID, sessionID, groupJID string, jids []string) error
}

// ChannelSvc is the channel/newsletter surface (§11 Channels).
type ChannelSvc interface {
	Create(ctx context.Context, tenantID, sessionID, name, description string) (string, error)
	Follow(ctx context.Context, tenantID, sessionID, jid string) error
	Unfollow(ctx context.Context, tenantID, sessionID, jid string) error
	Mute(ctx context.Context, tenantID, sessionID, jid string, mute bool) error
	Messages(ctx context.Context, tenantID, sessionID, jid, cursor string, limit int) (store.Page[domain.Message], error)
}

// StatusSvc is the status/stories surface (§11 Status).
type StatusSvc interface {
	PostText(ctx context.Context, tenantID, sessionID, text string) (string, error)
	PostImage(ctx context.Context, tenantID, sessionID string) (string, error)
}

// PresenceSvc is the account-wide presence surface (§11 Presence).
type PresenceSvc interface {
	Set(ctx context.Context, tenantID, sessionID, state string) error
}

// Compile-time proof that the concrete services satisfy the handler interfaces.
var (
	_ SessionSvc  = (*service.SessionService)(nil)
	_ MessageSvc  = (*service.MessageService)(nil)
	_ KeySvc      = (*service.KeyService)(nil)
	_ WebhookSvc  = (*service.WebhookService)(nil)
	_ AdminSvc    = (*service.AdminService)(nil)
	_ ChatSvc     = (*service.ChatService)(nil)
	_ ContactSvc  = (*service.ContactService)(nil)
	_ GroupSvc    = (*service.GroupService)(nil)
	_ ChannelSvc  = (*service.ChannelService)(nil)
	_ StatusSvc   = (*service.StatusService)(nil)
	_ PresenceSvc = (*service.PresenceService)(nil)
)

// Handlers bundles the service dependencies and exposes one http.HandlerFunc
// method per §11 endpoint. CORE groups (sessions, messages, keys, webhooks,
// events) are fully implemented; the remaining groups (chats, contacts, groups,
// channels, status, presence, admin*) are method stubs returning not_implemented
// until the next stage fills them.
type Handlers struct {
	Sessions SessionSvc
	Messages MessageSvc
	Keys     KeySvc
	Webhooks WebhookSvc
	Admin    AdminSvc
	Chats    ChatSvc
	Contacts ContactSvc
	Groups   GroupSvc
	Channels ChannelSvc
	Status   StatusSvc
	Presence PresenceSvc

	// EventStream is the live NDJSON stream handler (internal/stream). May be nil
	// in tests that don't exercise the stream endpoint.
	EventStream http.Handler

	Log *slog.Logger
}

// New builds Handlers from the concrete service aggregate and the stream
// handler. The concrete services satisfy the consumer interfaces above.
func New(s *service.Services, events http.Handler, log *slog.Logger) *Handlers {
	if log == nil {
		log = slog.Default()
	}
	return &Handlers{
		Sessions:    s.Sessions,
		Messages:    s.Messages,
		Keys:        s.Keys,
		Webhooks:    s.Webhooks,
		Admin:       s.Admin,
		Chats:       s.Chats,
		Contacts:    s.Contacts,
		Groups:      s.Groups,
		Channels:    s.Channels,
		Status:      s.Status,
		Presence:    s.Presence,
		EventStream: events,
		Log:         log,
	}
}

// ---------------------------------------------------------------------------
// Shared helpers.
// ---------------------------------------------------------------------------

// tenant pulls the authenticated tenant id from the request context (set by the
// API-key auth or cookie-session middleware). Returns false (and writes a 401)
// when absent.
func tenant(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := httpx.TenantID(r.Context())
	if id == "" {
		httpx.WriteError(w, domain.ErrUnauthorized("authentication required"))
		return "", false
	}
	return id, true
}

// param reads a chi URL parameter.
func param(r *http.Request, name string) string { return chi.URLParam(r, name) }
