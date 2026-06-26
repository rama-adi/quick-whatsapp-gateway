package controlbus

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
)

// stubVerifier resolves one raw key to a fixed api-key Principal and counts how
// many times it is consulted, so the real CachingKeyVerifier's eviction is
// observable (a cache miss re-delegates and bumps the count).
type stubVerifier struct {
	keyID, orgID string
	mu           sync.Mutex
	calls        int
}

func (s *stubVerifier) VerifyKey(context.Context, string) (*authz.Principal, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return &authz.Principal{Kind: authz.KindAPIKey, OrganizationID: s.orgID, KeyID: s.keyID}, nil
}

func (s *stubVerifier) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// fakeCache records eviction calls for assertions.
type fakeCache struct {
	mu            sync.Mutex
	evictedKeys   []string
	evictedUsers  []string
	evictedUserOg [][2]string
}

func (c *fakeCache) EvictKey(keyID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictedKeys = append(c.evictedKeys, keyID)
}

func (c *fakeCache) EvictUser(userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictedUsers = append(c.evictedUsers, userID)
}

func (c *fakeCache) EvictUserOrg(userID, orgID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictedUserOg = append(c.evictedUserOg, [2]string{userID, orgID})
}

func (c *fakeCache) keys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.evictedKeys...)
}

func (c *fakeCache) users() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.evictedUsers...)
}

func (c *fakeCache) userOrgs() [][2]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][2]string(nil), c.evictedUserOg...)
}

// fakeDropper records stream-drop calls.
type fakeDropper struct {
	mu             sync.Mutex
	droppedKeys    []string
	droppedUsers   []string
	droppedUserOrg [][2]string
}

func (d *fakeDropper) DropByKey(keyID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.droppedKeys = append(d.droppedKeys, keyID)
	return 1
}

func (d *fakeDropper) DropByUser(userID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.droppedUsers = append(d.droppedUsers, userID)
	return 1
}

func (d *fakeDropper) DropByUserOrg(userID, orgID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.droppedUserOrg = append(d.droppedUserOrg, [2]string{userID, orgID})
	return 1
}

func (d *fakeDropper) keys() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.droppedKeys...)
}

func newRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rc.Close()
		mr.Close()
	})
	return mr, rc
}

// waitFor polls cond until it returns true or the deadline elapses.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestSubscriber_APIKeyRevoked(t *testing.T) {
	_, rc := newRedis(t)
	cache := &fakeCache{}
	dropper := &fakeDropper{}
	sub := New(rc, cache, dropper, nil)
	if err := sub.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sub.Stop()

	if err := rc.Publish(context.Background(), ChannelAPIKeyRevoked, `{"keyId":"key_123","userId":"user_1"}`).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}

	waitFor(t, func() bool {
		keys := cache.keys()
		return len(keys) == 1 && keys[0] == "key_123"
	})
	if got := dropper.keys(); len(got) != 1 || got[0] != "key_123" {
		t.Fatalf("dropper keys = %v, want [key_123]", got)
	}
}

func TestSubscriber_UserBanned(t *testing.T) {
	_, rc := newRedis(t)
	cache := &fakeCache{}
	dropper := &fakeDropper{}
	sub := New(rc, cache, dropper, nil)
	if err := sub.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sub.Stop()

	if err := rc.Publish(context.Background(), ChannelUserBanned, `{"userId":"user_42"}`).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, func() bool {
		users := cache.users()
		return len(users) == 1 && users[0] == "user_42"
	})
}

func TestSubscriber_MemberRemoved(t *testing.T) {
	_, rc := newRedis(t)
	cache := &fakeCache{}
	dropper := &fakeDropper{}
	sub := New(rc, cache, dropper, nil)
	if err := sub.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sub.Stop()

	if err := rc.Publish(context.Background(), ChannelMemberRemoved, `{"userId":"user_9","organizationId":"org_x"}`).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, func() bool {
		uo := cache.userOrgs()
		return len(uo) == 1 && uo[0] == [2]string{"user_9", "org_x"}
	})
}

func TestSubscriber_MalformedAndMissingFieldsIgnored(t *testing.T) {
	_, rc := newRedis(t)
	cache := &fakeCache{}
	sub := New(rc, cache, nil, nil)
	if err := sub.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sub.Stop()

	// Malformed JSON and a revoked message missing keyId must not evict anything.
	_ = rc.Publish(context.Background(), ChannelAPIKeyRevoked, `not json`).Err()
	_ = rc.Publish(context.Background(), ChannelAPIKeyRevoked, `{"userId":"user_1"}`).Err()
	// A well-formed one afterwards proves the loop survived the bad ones.
	_ = rc.Publish(context.Background(), ChannelAPIKeyRevoked, `{"keyId":"key_ok"}`).Err()

	waitFor(t, func() bool {
		keys := cache.keys()
		return len(keys) == 1 && keys[0] == "key_ok"
	})
}

// TestSubscriber_EvictsRealCache is the end-to-end check requested by §4.6: a
// real CachingKeyVerifier is warmed, a ctrl:apikey.revoked is PUBLISHed via
// miniredis, and the cache entry is observed to be evicted (next VerifyKey
// re-delegates to the inner verifier).
func TestSubscriber_EvictsRealCache(t *testing.T) {
	_, rc := newRedis(t)

	const raw = "ba_live_key"
	inner := &stubVerifier{keyID: "key_live", orgID: "org_1"}
	cache := authz.NewCachingKeyVerifier(inner, authz.DefaultKeyCacheTTL)
	drops := &fakeDropper{}

	sub := New(rc, cache, drops, nil)
	if err := sub.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sub.Stop()

	// Warm the cache: first verify delegates once, second is a cached hit.
	if _, err := cache.VerifyKey(context.Background(), raw); err != nil {
		t.Fatalf("warm: %v", err)
	}
	if _, err := cache.VerifyKey(context.Background(), raw); err != nil {
		t.Fatalf("cached verify: %v", err)
	}
	if got := inner.callCount(); got != 1 {
		t.Fatalf("warm-up delegated %d times, want 1 (second should be cached)", got)
	}

	// Publish the revocation; wait for the stream-drop side effect to confirm the
	// subscriber processed the message in the same handler that called EvictKey.
	if err := rc.Publish(context.Background(), ChannelAPIKeyRevoked, `{"keyId":"key_live"}`).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, func() bool {
		ks := drops.keys()
		return len(ks) == 1 && ks[0] == "key_live"
	})

	// Entry was evicted: the next verify is a miss → re-delegates (count bumps).
	if _, err := cache.VerifyKey(context.Background(), raw); err != nil {
		t.Fatalf("post-evict verify: %v", err)
	}
	if got := inner.callCount(); got != 2 {
		t.Fatalf("post-evict delegated %d times total, want 2 (cache entry should have been evicted)", got)
	}
}

func TestSubscriber_StopIsClean(t *testing.T) {
	_, rc := newRedis(t)
	sub := New(rc, &fakeCache{}, &fakeDropper{}, nil)
	if err := sub.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	sub.Stop()
	// Stop again is a no-op (must not panic).
	sub.Stop()
}
