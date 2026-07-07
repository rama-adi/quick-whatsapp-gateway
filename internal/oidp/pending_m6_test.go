package oidp

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPendingLuaScriptsDoNotUseRedisKEYS(t *testing.T) {
	raw, err := os.ReadFile("pending.go")
	require.NoError(t, err)
	if strings.Contains(string(raw), `redis.call("KEYS"`) {
		t.Fatal("pending Lua scripts use redis KEYS")
	}
}

func TestClaimVerifiedConcurrentExactlyOneWinner(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "m6", 10*time.Minute)
	req := PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_1", SessionID: "sess_1", UserCode: "483920",
		LoginCommand: "login", Mode: "dm", AppName: "Acme", Status: PendingStatusPending,
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}
	require.NoError(t, ps.Create(context.Background(), req))

	const n = 24
	var wg sync.WaitGroup
	results := make(chan ClaimStatus, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := ps.ClaimVerified(context.Background(), ClaimInput{
				SessionID: "sess_1", UserCode: "483920", Mode: "dm", LoginCommand: "login",
				SenderLID: "111@lid", PhoneJID: "111@s.whatsapp.net", NowMs: time.Now().UnixMilli(),
			})
			require.NoError(t, err)
			results <- res.Status
		}()
	}
	wg.Wait()
	close(results)

	winners := 0
	for status := range results {
		if status == ClaimStatusVerified {
			winners++
		}
	}
	require.Equal(t, 1, winners)
	loaded, err := ps.Load(context.Background(), "browser_1")
	require.NoError(t, err)
	require.Equal(t, PendingStatusVerified, loaded.Status)
	require.NotNil(t, loaded.Verified)
}

func TestStopDeniesJustVerifiedRequest(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "m6stop", 10*time.Minute)
	req := PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_1", SessionID: "sess_1", UserCode: "483920",
		LoginCommand: "login", Mode: "dm", AppName: "Acme", Status: PendingStatusPending,
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}
	require.NoError(t, ps.Create(context.Background(), req))
	res, err := ps.ClaimVerified(context.Background(), ClaimInput{
		SessionID: "sess_1", UserCode: "483920", Mode: "dm", LoginCommand: "login",
		SenderLID: "111@lid", NowMs: time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	require.Equal(t, ClaimStatusVerified, res.Status)
	require.NoError(t, ps.RememberStop(context.Background(), "111@lid", "browser_1", time.Minute))

	res, err = ps.DenyRecentForSender(context.Background(), "111@lid")
	require.NoError(t, err)
	require.Equal(t, ClaimStatusDenied, res.Status)
	loaded, err := ps.Load(context.Background(), "browser_1")
	require.NoError(t, err)
	require.Equal(t, PendingStatusDenied, loaded.Status)
}

func TestWrongCodeSenderCap(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "m6rl", 10*time.Minute)
	var res ClaimResult
	for i := 0; i < 5; i++ {
		got, err := ps.ClaimWrongCode(context.Background(), "sess_1", "111@lid", time.Now().UnixMilli())
		require.NoError(t, err)
		res = got
	}
	require.Equal(t, ClaimStatusWrong, res.Status)
	res, err := ps.ClaimWrongCode(context.Background(), "sess_1", "111@lid", time.Now().UnixMilli())
	require.NoError(t, err)
	require.Equal(t, ClaimStatusRateLimited, res.Status)
}

func TestClaimDuplicateDeliveryReturnsAlreadyUsed(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "m6dup", 10*time.Minute)
	require.NoError(t, ps.Create(context.Background(), PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_1", SessionID: "sess_1", UserCode: "483920",
		LoginCommand: "login", Mode: "dm", AppName: "Acme", Status: PendingStatusPending,
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}))
	input := ClaimInput{SessionID: "sess_1", UserCode: "483920", Mode: "dm", LoginCommand: "login", SenderLID: "111@lid", NowMs: time.Now().UnixMilli()}
	res, err := ps.ClaimVerified(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, ClaimStatusVerified, res.Status)
	res, err = ps.ClaimVerified(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, ClaimStatusAlreadyUsed, res.Status)
}

func TestClaimModeMismatchAttemptCapDeniesRequest(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "m6cap", 10*time.Minute)
	require.NoError(t, ps.Create(context.Background(), PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_1", SessionID: "sess_1", UserCode: "483920",
		LoginCommand: "login", Mode: "group", AppName: "Acme", Status: PendingStatusPending,
		Attempts: 9, ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}))
	res, err := ps.ClaimVerified(context.Background(), ClaimInput{
		SessionID: "sess_1", UserCode: "483920", Mode: "dm", LoginCommand: "login",
		SenderLID: "111@lid", NowMs: time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	require.Equal(t, ClaimStatusDenied, res.Status)
	require.Equal(t, 10, res.Attempts)
	loaded, err := ps.Load(context.Background(), "browser_1")
	require.NoError(t, err)
	require.Equal(t, PendingStatusDenied, loaded.Status)
}

func TestInvalidateSessionExpiresOpenPendingStreams(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "m8stream", 10*time.Minute)
	require.NoError(t, ps.Create(context.Background(), PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_1", SessionID: "sess_1", UserCode: "483920",
		LoginCommand: "login", Mode: "dm", AppName: "Acme", Status: PendingStatusPending,
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}))
	pubsub := ps.Subscribe(context.Background(), "browser_1")
	defer pubsub.Close()
	_, err := pubsub.ReceiveTimeout(context.Background(), time.Second)
	require.NoError(t, err)

	p := NewProvider(ProviderConfig{Pending: ps})
	p.InvalidateSession("sess_1")

	msg, err := pubsub.ReceiveMessage(context.Background())
	require.NoError(t, err)
	require.Equal(t, PendingStatusExpired, msg.Payload)
	_, err = ps.Load(context.Background(), "browser_1")
	require.Error(t, err)
}

func TestDenyClientPendingPublishesDenied(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "m8deny", 10*time.Minute)
	require.NoError(t, ps.Create(context.Background(), PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_1", SessionID: "sess_1", UserCode: "483920",
		LoginCommand: "login", Mode: "dm", AppName: "Acme", Status: PendingStatusPending,
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}))
	require.NoError(t, ps.Create(context.Background(), PendingRequest{
		ClientID: "client_2", BrowserCode: "browser_2", SessionID: "sess_1", UserCode: "483921",
		LoginCommand: "login", Mode: "dm", AppName: "Other", Status: PendingStatusPending,
		ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	}))
	pubsub := ps.Subscribe(context.Background(), "browser_1")
	defer pubsub.Close()
	_, err := pubsub.ReceiveTimeout(context.Background(), time.Second)
	require.NoError(t, err)

	n, err := ps.DenyClientPending(context.Background(), "client_1")
	require.NoError(t, err)
	require.Equal(t, 1, n)
	msg, err := pubsub.ReceiveMessage(context.Background())
	require.NoError(t, err)
	require.Equal(t, PendingStatusDenied, msg.Payload)
	denied, err := ps.Load(context.Background(), "browser_1")
	require.NoError(t, err)
	require.Equal(t, PendingStatusDenied, denied.Status)
	other, err := ps.Load(context.Background(), "browser_2")
	require.NoError(t, err)
	require.Equal(t, PendingStatusPending, other.Status)
}
