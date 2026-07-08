package oidp

import (
	"context"
	"testing"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/inbound"
	"github.com/stretchr/testify/require"
)

type fakeActiveApps struct {
	apps  []domain.OAuthClient
	calls int
}

func (f *fakeActiveApps) ListActiveBySession(context.Context, string) ([]domain.OAuthClient, error) {
	f.calls++
	return f.apps, nil
}

type fakeMembers struct{ ok bool }

func (f fakeMembers) IsActiveGroupMember(context.Context, string, string, string) (bool, error) {
	return f.ok, nil
}

type fakeBot struct {
	reactions []string
	replies   []string
}

func (f *fakeBot) React(_ context.Context, _, _, _, _, _, emoji string) error {
	f.reactions = append(f.reactions, emoji)
	return nil
}

func (f *fakeBot) Reply(_ context.Context, _, _, _, _ string, text string) error {
	f.replies = append(f.replies, text)
	return nil
}

func TestLoginInterceptorMatchingMatrix(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "lim", 10*time.Minute)
	apps := &fakeActiveApps{apps: []domain.OAuthClient{{
		ClientID: "client_1", SessionID: "sess_1", Name: "Acme", LoginCommand: "login",
		Modes: "dm,group", GroupJID: strp("120@g.us"), Status: "active",
	}}}
	bot := &fakeBot{}
	li := NewLoginInterceptor(apps, ps, fakeMembers{ok: true}, bot, nil)
	require.NoError(t, ps.Create(context.Background(), PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_1", SessionID: "sess_1", UserCode: "483920",
		LoginCommand: "login", Mode: "dm", AppName: "Acme", Status: PendingStatusPending,
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}))

	handled, err := li.HandleLogin(context.Background(), loginNM("  LOGIN 483920  ", true, false))
	require.NoError(t, err)
	require.True(t, handled)
	require.Contains(t, bot.reactions, "✅")
	require.Len(t, bot.replies, 1)

	fromMe := loginNM("login 111111", true, false)
	fromMe.FromMe = true
	handled, err = li.HandleLogin(context.Background(), fromMe)
	require.NoError(t, err)
	require.False(t, handled)

	handled, err = li.HandleLogin(context.Background(), loginNM("hello 483920", true, false))
	require.NoError(t, err)
	require.False(t, handled)

	group := loginNM("login 222222", false, true)
	group.ChatJID = "999@g.us"
	group.Mentions = []string{"bot@s.whatsapp.net"}
	group.SelfJID = "bot@s.whatsapp.net"
	reactionsBefore := len(bot.reactions)
	handled, err = li.HandleLogin(context.Background(), group)
	require.NoError(t, err)
	require.True(t, handled)
	require.Greater(t, len(bot.reactions), reactionsBefore)
	require.NotEqual(t, "✅", bot.reactions[len(bot.reactions)-1])

	group = loginNM("login 222222", false, true)
	group.ChatJID = "120@g.us"
	group.Mentions = nil
	group.SelfJID = "bot@s.whatsapp.net"
	handled, err = li.HandleLogin(context.Background(), group)
	require.NoError(t, err)
	require.True(t, handled)
}

func TestLoginInterceptorGroupRequiresBotMention(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "libotmention", 10*time.Minute)
	apps := &fakeActiveApps{apps: []domain.OAuthClient{{
		ClientID: "client_1", SessionID: "sess_1", Name: "Acme", LoginCommand: "login",
		Modes: "group", GroupJID: strp("120@g.us"), Status: "active",
	}}}
	li := NewLoginInterceptor(apps, ps, fakeMembers{ok: true}, nil, nil)
	require.NoError(t, ps.Create(context.Background(), PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_1", SessionID: "sess_1", UserCode: "483920",
		LoginCommand: "login", Mode: "group", AppName: "Acme", Status: PendingStatusPending,
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}))

	other := loginNM("login 483920", false, true)
	other.ChatJID = "120@g.us"
	other.SelfJID = "628000@s.whatsapp.net"
	other.SelfLID = "999@lid"
	other.Mentions = []string{"111@lid"}
	handled, err := li.HandleLogin(context.Background(), other)
	require.NoError(t, err)
	require.True(t, handled)
	req, err := ps.Load(context.Background(), "browser_1")
	require.NoError(t, err)
	require.Equal(t, PendingStatusPending, req.Status)

	botLID := loginNM("login 483920", false, true)
	botLID.ChatJID = "120@g.us"
	botLID.SelfJID = "628000@s.whatsapp.net"
	botLID.SelfLID = "999@lid"
	botLID.Mentions = []string{"999@lid"}
	handled, err = li.HandleLogin(context.Background(), botLID)
	require.NoError(t, err)
	require.True(t, handled)
	req, err = ps.Load(context.Background(), "browser_1")
	require.NoError(t, err)
	require.Equal(t, PendingStatusVerified, req.Status)

	require.NoError(t, ps.Create(context.Background(), PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_2", SessionID: "sess_1", UserCode: "483921",
		LoginCommand: "login", Mode: "group", AppName: "Acme", Status: PendingStatusPending,
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}))
	botJID := loginNM("login 483920", false, true)
	botJID.Body = "login 483921"
	botJID.ChatJID = "120@g.us"
	botJID.SelfJID = "628000@s.whatsapp.net"
	botJID.Mentions = []string{"628000@s.whatsapp.net"}
	handled, err = li.HandleLogin(context.Background(), botJID)
	require.NoError(t, err)
	require.True(t, handled)
	req, err = ps.Load(context.Background(), "browser_2")
	require.NoError(t, err)
	require.Equal(t, PendingStatusVerified, req.Status)
}

func TestLoginInterceptorGroupJIDCanonicalComparison(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "lijidcanon", 10*time.Minute)
	apps := &fakeActiveApps{apps: []domain.OAuthClient{{
		ClientID: "client_1", SessionID: "sess_1", Name: "Acme", LoginCommand: "login",
		Modes: "group", GroupJID: strp("120363@g.us"), Status: "active",
	}}}
	li := NewLoginInterceptor(apps, ps, fakeMembers{ok: true}, nil, nil)

	cases := []struct {
		name       string
		code       string
		chatJID    string
		bodyPrefix string
		selfJID    string
		selfLID    string
		mentions   []string
	}{
		{
			name: "device suffixed group and mention",
			code: "483930", chatJID: "120363:7@g.us",
			selfJID: "628000:3@s.whatsapp.net", mentions: []string{"628000@s.whatsapp.net"},
		},
		{
			name: "bare group and mention",
			code: "483931", chatJID: "120363",
			selfJID: "628000@s.whatsapp.net", mentions: []string{"628000"},
		},
		{
			name: "lid mention",
			code: "483932", chatJID: "120363@g.us",
			selfJID: "628000@s.whatsapp.net", selfLID: "205227043110953@lid", mentions: []string{"205227043110953:12@lid"},
		},
		{
			name: "mention token in body",
			code: "483933", chatJID: "120363@g.us", bodyPrefix: "@628000 ",
			selfJID: "628000@s.whatsapp.net", mentions: []string{"628000@s.whatsapp.net"},
		},
		{
			name: "lid mention token in body",
			code: "483934", chatJID: "120363@g.us", bodyPrefix: "@205227043110953 ",
			selfJID: "628000@s.whatsapp.net", selfLID: "205227043110953@lid", mentions: []string{"205227043110953@lid"},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			browserCode := "browser_" + tt.code
			require.NoError(t, ps.Create(context.Background(), PendingRequest{
				ClientID: "client_1", BrowserCode: browserCode, SessionID: "sess_1", UserCode: tt.code,
				LoginCommand: "login", Mode: "group", AppName: "Acme", Status: PendingStatusPending,
				ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
			}))

			nm := loginNM(tt.bodyPrefix+"login "+tt.code, false, true)
			nm.ChatJID = tt.chatJID
			nm.SelfJID = tt.selfJID
			nm.SelfLID = tt.selfLID
			nm.Mentions = tt.mentions
			handled, err := li.HandleLogin(context.Background(), nm)
			require.NoError(t, err)
			require.True(t, handled)

			req, err := ps.Load(context.Background(), browserCode)
			require.NoError(t, err)
			require.Equal(t, PendingStatusVerified, req.Status)
		})
	}
}

func TestLoginInterceptorGroupJIDCanonicalComparisonIgnoresWrongTarget(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "lijidcanonneg", 10*time.Minute)
	apps := &fakeActiveApps{apps: []domain.OAuthClient{{
		ClientID: "client_1", SessionID: "sess_1", Name: "Acme", LoginCommand: "login",
		Modes: "group", GroupJID: strp("120363@g.us"), Status: "active",
	}}}
	li := NewLoginInterceptor(apps, ps, fakeMembers{ok: true}, nil, nil)

	require.NoError(t, ps.Create(context.Background(), PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_other_mention", SessionID: "sess_1", UserCode: "483940",
		LoginCommand: "login", Mode: "group", AppName: "Acme", Status: PendingStatusPending,
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}))
	nm := loginNM("login 483940", false, true)
	nm.ChatJID = "120363@g.us"
	nm.SelfJID = "628000@s.whatsapp.net"
	nm.SelfLID = "205227043110953@lid"
	nm.Mentions = []string{"111@lid"}
	handled, err := li.HandleLogin(context.Background(), nm)
	require.NoError(t, err)
	require.True(t, handled)
	req, err := ps.Load(context.Background(), "browser_other_mention")
	require.NoError(t, err)
	require.Equal(t, PendingStatusPending, req.Status)

	require.NoError(t, ps.Create(context.Background(), PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_other_group", SessionID: "sess_1", UserCode: "483941",
		LoginCommand: "login", Mode: "group", AppName: "Acme", Status: PendingStatusPending,
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}))
	nm = loginNM("login 483941", false, true)
	nm.ChatJID = "999@g.us"
	nm.SelfJID = "628000@s.whatsapp.net"
	nm.Mentions = []string{"628000@s.whatsapp.net"}
	handled, err = li.HandleLogin(context.Background(), nm)
	require.NoError(t, err)
	require.True(t, handled)
	req, err = ps.Load(context.Background(), "browser_other_group")
	require.NoError(t, err)
	require.Equal(t, PendingStatusPending, req.Status)
}

func TestLoginInterceptorInvalidatesCommandCache(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "licache", 10*time.Minute)
	apps := &fakeActiveApps{apps: []domain.OAuthClient{{ClientID: "c1", SessionID: "sess_1", Name: "Acme", LoginCommand: "login", Modes: "dm"}}}
	li := NewLoginInterceptor(apps, ps, nil, nil, nil)

	handled, err := li.HandleLogin(context.Background(), loginNM("masuk 123456", true, false))
	require.NoError(t, err)
	require.False(t, handled)
	require.Equal(t, 1, apps.calls)

	apps.apps = []domain.OAuthClient{{ClientID: "c1", SessionID: "sess_1", Name: "Acme", LoginCommand: "masuk", Modes: "dm"}}
	handled, err = li.HandleLogin(context.Background(), loginNM("masuk 123456", true, false))
	require.NoError(t, err)
	require.False(t, handled, "cached old command set still applies before invalidation")

	li.InvalidateSession("sess_1")
	handled, err = li.HandleLogin(context.Background(), loginNM("masuk 123456", true, false))
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, 2, apps.calls)
}

func loginNM(body string, dm, group bool) *inbound.NormalizedMessage {
	return &inbound.NormalizedMessage{
		Kind: inbound.KindMessage, SessionID: "sess_1", OrganizationID: "org_1",
		ChatJID: "111@s.whatsapp.net", IsDM: dm, IsGroup: group, Body: body,
		WAMessageID: "MSG1", SenderLID: "111@lid", SenderJID: "111@s.whatsapp.net", SenderPhone: "111",
	}
}
