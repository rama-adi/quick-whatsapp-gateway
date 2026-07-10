package oidp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPendingLuaScriptsDoNotUseRedisKEYS scans the Lua source used by PendingStore and rejects any
// redis.call of KEYS. Pending cleanup and claims must use bounded keys or incremental SCAN rather than
// blocking the Redis server over the whole keyspace. This structural test protects production latency as
// pending volume grows.
func TestPendingLuaScriptsDoNotUseRedisKEYS(t *testing.T) {
	raw, err := os.ReadFile("pending.go")
	require.NoError(t, err)
	if strings.Contains(string(raw), `redis.call("KEYS"`) {
		t.Fatal("pending Lua scripts use redis KEYS")
	}
}

// TestClaimVerifiedConcurrentExactlyOneWinner releases 24 goroutines to claim the same session and user
// code through the Redis Lua transition. Exactly one caller must receive verified; every other caller
// observes the used terminal state, and the stored request contains one VerifiedBlock. This proves
// verification has one linearization point under concurrency.
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

// TestCancelRacingVerificationNeverOverwritesWinner starts cancellation and WhatsApp verification from the
// same barrier for a fresh request, repeating the race to exercise both Redis orderings. If verification
// wins, cancellation must observe the verified terminal state without replacing it; if cancellation wins,
// the claim reports denied or expired depending on whether it read the request or the removed reverse index.
// This pins the Lua compare-and-set as the sole owner of the pending-to-terminal transition and guards against
// the former load/modify/store lost-update race.
func TestCancelRacingVerificationNeverOverwritesWinner(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "m6-cancel-race", 10*time.Minute)

	for i := 0; i < 50; i++ {
		browserCode := fmt.Sprintf("browser_%d", i)
		userCode := fmt.Sprintf("%06d", 400000+i)
		require.NoError(t, ps.Create(context.Background(), PendingRequest{
			ClientID: "client_1", BrowserCode: browserCode, SessionID: "sess_1", UserCode: userCode,
			LoginCommand: "login", Mode: "dm", Status: PendingStatusPending,
			ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
		}))

		start := make(chan struct{})
		claimResult := make(chan ClaimResult, 1)
		cancelResult := make(chan bool, 1)
		errs := make(chan error, 2)
		go func() {
			<-start
			res, err := ps.ClaimVerified(context.Background(), ClaimInput{
				SessionID: "sess_1", UserCode: userCode, Mode: "dm", LoginCommand: "login",
				SenderLID: "111@lid", NowMs: time.Now().UnixMilli(),
			})
			claimResult <- res
			errs <- err
		}()
		go func() {
			<-start
			ok, err := ps.Cancel(context.Background(), browserCode)
			cancelResult <- ok
			errs <- err
		}()
		close(start)

		claim := <-claimResult
		cancelled := <-cancelResult
		require.NoError(t, <-errs)
		require.NoError(t, <-errs)
		require.True(t, cancelled)
		stored, err := ps.Load(context.Background(), browserCode)
		require.NoError(t, err)
		switch claim.Status {
		case ClaimStatusVerified:
			require.Equal(t, PendingStatusVerified, stored.Status)
		case ClaimStatusDenied, ClaimStatusExpired:
			require.Equal(t, PendingStatusDenied, stored.Status)
		default:
			t.Fatalf("iteration %d: unexpected claim status %q with stored state %q", i, claim.Status, stored.Status)
		}
	}
}

// TestStopDeniesJustVerifiedRequest verifies a DM request, remembers it as the senders recent flow, then
// processes STOP. The verified request must transition to denied and remain loadable with that status.
// This gives the user a short post-verification cancellation window before browser finalization.
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

// TestWrongCodeSenderCap submits wrong codes from one sender through the per-sender Redis counter. The
// first five attempts report wrong, while the sixth reports rate_limited. The cap slows brute force
// without incrementing an unrelated pending requests own attempt counter.
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

// TestClaimDuplicateDeliveryReturnsAlreadyUsed delivers the same valid WhatsApp login command twice. The
// first claim verifies the request and replaces the reverse index with a short used marker; the duplicate
// must return already_used rather than verify again. This makes inbound event redelivery harmless.
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

// TestClaimModeMismatchAttemptCapDeniesRequest starts a request at nine failed attempts and claims its
// code from the wrong DM/group mode. The tenth mismatch must atomically mark the request denied, delete
// the reverse index, and report attempts=10. This bounds targeted guessing against one pending flow.
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

// TestClaimWrongAttemptDoesNotExtendRequestTTL records the Redis TTL before and after a mode-mismatch
// claim. Updating the attempt counter must preserve the original absolute expires_at rather than resetting
// the full request lifetime. Attackers therefore cannot keep a login prompt alive by sending wrong
// commands.
func TestClaimWrongAttemptDoesNotExtendRequestTTL(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "m6ttl", 10*time.Minute)
	expiresAt := time.Now().Add(2 * time.Second).UnixMilli()
	require.NoError(t, ps.Create(context.Background(), PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_1", SessionID: "sess_1", UserCode: "483920",
		LoginCommand: "login", Mode: "dm", AppName: "Acme", Status: PendingStatusPending,
		ExpiresAt: expiresAt,
	}))
	before := rdb.PTTL(context.Background(), ps.reqKey("browser_1")).Val()
	_, err := ps.ClaimVerified(context.Background(), ClaimInput{
		SessionID: "sess_1", UserCode: "483920", Mode: "group", LoginCommand: "login",
		SenderLID: "111@lid", NowMs: time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	after := rdb.PTTL(context.Background(), ps.reqKey("browser_1")).Val()
	require.LessOrEqual(t, after, before+100*time.Millisecond)
	require.Less(t, after, 10*time.Second)
}

// TestClaimAfterExpiresAtFailsExpired passes a logical claim time beyond the requests embedded expires_at
// even while the Redis key is still readable. ClaimVerified must report expired and remove the reverse
// user-code index. The embedded deadline is an independent safety check against Redis clock or TTL lag.
func TestClaimAfterExpiresAtFailsExpired(t *testing.T) {
	_, rdb := testRedis(t)
	ps := NewPendingStore(rdb, "m6expired", 10*time.Minute)
	now := time.Now()
	require.NoError(t, ps.Create(context.Background(), PendingRequest{
		ClientID: "client_1", BrowserCode: "browser_1", SessionID: "sess_1", UserCode: "483920",
		LoginCommand: "login", Mode: "dm", AppName: "Acme", Status: PendingStatusPending,
		ExpiresAt: now.Add(time.Second).UnixMilli(),
	}))
	res, err := ps.ClaimVerified(context.Background(), ClaimInput{
		SessionID: "sess_1", UserCode: "483920", Mode: "dm", LoginCommand: "login",
		SenderLID: "111@lid", NowMs: now.Add(2 * time.Second).UnixMilli(),
	})
	require.NoError(t, err)
	require.Equal(t, ClaimStatusExpired, res.Status)
}

// TestInvalidateSessionExpiresOpenPendingStreams creates a pending request, subscribes to its browser
// channel, and invalidates the owning session through Provider. The request becomes unavailable and the
// subscriber receives expired. This ensures session deletion or reassignment closes browser waits
// immediately.
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

// TestDenyClientPendingPublishesDenied creates pending requests for two clients and denies only one client
// through a control-bus-style scan. Exactly the matching request transitions to denied and publishes that
// status; the other remains pending. This pins client-scoped invalidation without collateral cancellation.
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
