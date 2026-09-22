// Package service holds the gateway's business services: the layer between the
// thin HTTP handlers (internal/http/handlers) and the persistence/subsystem
// layer (internal/store, internal/wa, internal/stream, internal/webhooks).
// Handlers validate + decode, call a service, and encode the
// result; services own the logic and orchestration; repos own the SQL.
//
// Every service is constructor-injected with its collaborators (no globals).
// The Services aggregate bundles them all behind one struct so the composition
// root (cmd/gateway) wires once and the router receives a single dependency.
package service

import (
	"context"
	"log/slog"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/crypto"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

// Deps groups everything the service layer needs from the composition root. The
// concrete types are constructed in cmd/gateway and handed in; the service
// package never opens a DB, a Redis client, or a whatsmeow client itself.
type Deps struct {
	Store                      *store.Store
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

// New builds every service from shared immutable dependencies and is the single
// business-layer wiring point. It starts no goroutines; resource services are
// safe to share because request state travels through context and repositories.
// Live WhatsApp operations are wired later by the API composition root through
// the gateway facades; until then live calls fail with the not_implemented
// envelope contract.
func New(d Deps) *Services {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	services := &Services{
		Sessions: NewSessionService(d.Store.Sessions, d.Store.Gateways, d.Log),
		Messages: NewMessageService(d.Store.Sessions, d.Log),
		Webhooks: NewWebhookService(d.Store.Webhooks, d.Crypto, d.DefaultRetryDelay, d.DefaultRetryAttempts, d.Log),
		Chats:    NewChatService(d.Store, nil, d.Log),
		Contacts: NewContactService(d.Store, nil, d.Log),
		Groups:   NewGroupService(d.Store, nil, d.Log),
		Channels: NewChannelService(d.Store, nil, d.Log),
		Status:   NewStatusService(d.Store, nil, d.Log),
		Presence: NewPresenceService(d.Store, nil, d.Log),
		Admin:    NewAdminService(d.Store, nil, d.Log),
		Events:   NewEventsService(d.Store.EventLog, d.Log),
		Backup:   NewBackupImportService(d.Store, d.Log),
		OAuthApps: NewOAuthAppService(
			d.Store,
			d.OAuthClientSecretPepper,
			d.WhatsAppAdminCommandPrefix,
			d.OIDCIssuer,
			d.ControlPublisher,
		),
	}
	services.Sessions.SetOAuthCascader(services.OAuthApps)
	return services
}

// ControlPublisher broadcasts cache invalidation and revocation after the SQL
// mutation commits. Publishing is best-effort at service call sites because the
// database remains authoritative on reconnect/cache expiry.
type ControlPublisher interface {
	Publish(ctx context.Context, channel string, payload any) error
}
