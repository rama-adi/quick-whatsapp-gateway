package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/oidp"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type eventConsumerApps struct{ apps []domain.OAuthClient }

func (f eventConsumerApps) ListActiveBySession(context.Context, string) ([]domain.OAuthClient, error) {
	return f.apps, nil
}

type eventConsumerBot struct{ reactions, replies []string }

func (f *eventConsumerBot) React(context.Context, string, string, string, string, string, string) error {
	f.reactions = append(f.reactions, "reaction")
	return nil
}

func (f *eventConsumerBot) Reply(_ context.Context, _, _, _, _, text string) error {
	f.replies = append(f.replies, text)
	return nil
}

func TestNormalizedLoginMessageFromCommittedEvent(t *testing.T) {
	event := domain.NewEvent(domain.EventMessage, "sess_1", "org_1", nil)
	nm := normalizedLoginMessage(event, apitypes.MessagePayload{
		WAMessageID: "wam_1",
		ChatJID:     "120@g.us",
		SenderJID:   "628123@s.whatsapp.net",
		SenderLID:   "123@lid",
		Type:        "text",
		Body:        "login 123456",
		PushName:    "Alice",
		Mentions: map[string]apitypes.MentionData{
			"628999@s.whatsapp.net": {},
		},
		Timestamp: 99,
	})

	require.Equal(t, "sess_1", nm.SessionID)
	require.Equal(t, "org_1", nm.OrganizationID)
	require.True(t, nm.IsGroup)
	require.False(t, nm.IsDM)
	require.Equal(t, "123@lid", nm.SenderLID)
	require.Equal(t, "628123", nm.SenderPhone)
	require.Equal(t, []string{"628999@s.whatsapp.net"}, nm.Mentions)
}

func TestOIDPEventConsumerIgnoresNonInboundMessages(t *testing.T) {
	consumer := NewOIDPEventConsumer(nil)
	event := domain.NewEvent(domain.EventMessageFromMe, "sess_1", "org_1", apitypes.MessagePayload{
		FromMe: true,
	})
	require.NoError(t, consumer.ConsumeCommittedEvent(t.Context(), event))
}

func TestOIDPEventConsumerClaimsBeforeProjectionFanout(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	pending := oidp.NewPendingStore(rdb, "event-consumer", 10*time.Minute)
	require.NoError(t, pending.Create(t.Context(), oidp.PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_1", SessionID: "sess_1", UserCode: "123456",
		LoginCommand: "login", Mode: oidp.ModeDM, AppName: "Acme", Status: oidp.PendingStatusPending,
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}))
	bot := &eventConsumerBot{}
	interceptor := oidp.NewLoginInterceptor(
		eventConsumerApps{apps: []domain.OAuthClient{{
			ClientID: "client_1", SessionID: "sess_1", Name: "Acme", LoginCommand: "login", Modes: "dm", Status: "active",
		}}},
		pending,
		nil,
		bot,
		nil,
	)
	login := NewOIDPEventConsumer(interceptor)
	projection := &recordingCommittedConsumer{}
	publisher := &recordingCommittedPublisher{}
	webhooks := &recordingCommittedWebhooks{}
	event := domain.NewEvent(domain.EventMessage, "sess_1", "org_1", apitypes.MessagePayload{
		WAMessageID: "wam_1", ChatJID: "628123@s.whatsapp.net", SenderJID: "628123@s.whatsapp.net",
		SenderLID: "123@lid", Type: "text", Body: "login 123456",
	})
	store := &durableCommittedEventStore{
		committed: map[string]domain.Event{event.ID: event},
		completed: map[string]bool{},
	}
	worker := newTestCommittedEventWorker(store, NewCommittedEventDispatcher(
		[]application.CommittedEventConsumer{NewCommittedEventConsumers(login, projection)},
		publisher,
		webhooks,
	))

	completed, err := worker.RunOnce(t.Context(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, completed)
	require.Equal(t, 0, projection.count())
	require.Equal(t, 0, publisher.count())
	require.Empty(t, webhooks.calls)
	require.Len(t, bot.reactions, 1)
	claimed, err := pending.Load(t.Context(), "browser_1")
	require.NoError(t, err)
	require.Equal(t, oidp.PendingStatusVerified, claimed.Status)
}
