// Package service holds the gateway's business services: the layer between the
// thin HTTP handlers (internal/http/handlers) and the persistence/subsystem
// layer (internal/store, internal/wa, internal/stream, internal/webhooks).
// Handlers validate + decode, call a service, and encode the
// result; services own the logic and orchestration; repos own the SQL.
//
// Every service is constructor-injected with its collaborators (no globals).
// The Services aggregate bundles them all behind one struct so the composition
// root (cmd/server) wires once and the router receives a single dependency.
package service

import (
	"context"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/crypto"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// Deps groups everything the service layer needs from the composition root. The
// concrete types are constructed in cmd/server and handed in; the service
// package never opens a DB, a Redis client, or a whatsmeow client itself.
type Deps struct {
	Store                      *store.Store
	Manager                    *wa.Manager
	Sender                     *outbound.Sender
	Crypto                     *crypto.AESGCM
	OAuthClientSecretPepper    string
	OIDCIssuer                 string
	WhatsAppAdminCommandPrefix string
	ControlPublisher           ControlPublisher

	// DefaultRetryDelay / DefaultRetryAttempts seed a webhook's retry policy when
	// the caller does not specify one (WEBHOOK_RETRIES_* config defaults).
	DefaultRetryDelay    int
	DefaultRetryAttempts int

	Log *slog.Logger
}

// Services is the aggregate of every business service, wired from Deps. The
// router holds one of these and reads the field it needs per handler.
type Services struct {
	Sessions  *SessionService
	Messages  *MessageService
	Webhooks  *WebhookService
	Chats     *ChatService
	Contacts  *ContactService
	Groups    *GroupService
	Channels  *ChannelService
	Status    *StatusService
	Presence  *PresenceService
	Admin     *AdminService
	Events    *EventsService
	Backup    *BackupImportService
	OAuthApps *OAuthAppService
}

// New builds every service from the shared Deps. It is the single wiring point
// for the business layer.
func New(d Deps) *Services {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	// The manager-backed live-ops adapter satisfies every live port (GroupOps,
	// ContactDirectory, PresenceController, ChannelOps, StatusPoster). One value
	// serves all the resource services. nil manager => nil adapter, and the
	// services fall back to the not_implemented envelope on live calls.
	var live *wa.LiveOps
	if d.Manager != nil {
		live = d.Manager.LiveOps()
	}
	return &Services{
		Sessions:  NewSessionService(d.Store.Sessions, d.Manager, d.Log),
		Messages:  NewMessageService(d.Store.Sessions, d.Sender, d.Log),
		Webhooks:  NewWebhookService(d.Store.Webhooks, d.Crypto, d.DefaultRetryDelay, d.DefaultRetryAttempts, d.Log),
		Chats:     NewChatService(d.Store, liveOrNilPresence(live), d.Log),
		Contacts:  NewContactService(d.Store, liveOrNilDirectory(live), d.Log),
		Groups:    NewGroupService(d.Store, liveOrNilGroupOps(live), d.Log),
		Channels:  NewChannelService(d.Store, liveOrNilChannelOps(live), d.Log),
		Status:    NewStatusService(d.Store, liveOrNilStatusPoster(live), d.Log),
		Presence:  NewPresenceService(d.Store, liveOrNilPresence(live), d.Log),
		Admin:     NewAdminService(d.Store, liveOrNilBackfill(live), d.Log),
		Events:    NewEventsService(d.Store.EventLog, d.Log),
		Backup:    NewBackupImportService(d.Store, d.Log),
		OAuthApps: NewOAuthAppService(d.Store, d.OAuthClientSecretPepper, d.WhatsAppAdminCommandPrefix, d.OIDCIssuer, d.ControlPublisher),
	}
}

type ControlPublisher interface {
	Publish(ctx context.Context, channel string, payload any) error
}

// The liveOrNil* helpers convert a possibly-nil *wa.LiveOps into a typed nil
// interface so a nil adapter stays a nil interface (avoiding a non-nil interface
// wrapping a nil pointer, which would slip past the services' nil checks).
func liveOrNilGroupOps(l *wa.LiveOps) GroupOps {
	if l == nil {
		return nil
	}
	return l
}

func liveOrNilDirectory(l *wa.LiveOps) ContactDirectory {
	if l == nil {
		return nil
	}
	return l
}

func liveOrNilPresence(l *wa.LiveOps) PresenceController {
	if l == nil {
		return nil
	}
	return l
}

func liveOrNilChannelOps(l *wa.LiveOps) ChannelOps {
	if l == nil {
		return nil
	}
	return l
}

func liveOrNilStatusPoster(l *wa.LiveOps) StatusPoster {
	if l == nil {
		return nil
	}
	return l
}

func liveOrNilBackfill(l *wa.LiveOps) BackfillSource {
	if l == nil {
		return nil
	}
	return l
}
