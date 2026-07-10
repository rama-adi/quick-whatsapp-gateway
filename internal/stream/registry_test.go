package stream

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestConnRegistry_DropByKey registers two cancellable streams in the same organization under different
// API keys. Dropping one key must cancel and remove only its stream, leaving the unrelated context live.
// This is the immediate revocation path before a reconnect revalidates credentials.
func TestConnRegistry_DropByKey(t *testing.T) {
	r := NewConnRegistry()
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelA()
	defer cancelB()

	r.register(ConnIdentity{KeyID: "key_a", OrganizationID: "org_1"}, cancelA)
	r.register(ConnIdentity{KeyID: "key_b", OrganizationID: "org_1"}, cancelB)

	if n := r.DropByKey("key_a"); n != 1 {
		t.Fatalf("DropByKey dropped %d, want 1", n)
	}
	select {
	case <-ctxA.Done():
	default:
		t.Fatal("conn A context should be cancelled after DropByKey")
	}
	select {
	case <-ctxB.Done():
		t.Fatal("conn B must not be cancelled by an unrelated key")
	default:
	}
}

// TestConnRegistry_DropByUserAndUserOrg registers one user in two organizations plus an unrelated user.
// Membership removal first cancels only the matching user-organization pair; a later user ban cancels the
// remaining stream across organizations. The sequence pins the different blast radii of member removal and
// global user revocation.
func TestConnRegistry_DropByUserAndUserOrg(t *testing.T) {
	r := NewConnRegistry()
	ctx1, c1 := context.WithCancel(context.Background())
	ctx2, c2 := context.WithCancel(context.Background())
	ctx3, c3 := context.WithCancel(context.Background())
	defer c1()
	defer c2()
	defer c3()

	r.register(ConnIdentity{UserID: "u1", OrganizationID: "orgA"}, c1)
	r.register(ConnIdentity{UserID: "u1", OrganizationID: "orgB"}, c2)
	r.register(ConnIdentity{UserID: "u2", OrganizationID: "orgA"}, c3)

	// member.removed: only u1's orgA stream drops; u1's orgB stays.
	if n := r.DropByUserOrg("u1", "orgA"); n != 1 {
		t.Fatalf("DropByUserOrg dropped %d, want 1", n)
	}
	assertDone(t, ctx1, true, "u1/orgA")
	assertDone(t, ctx2, false, "u1/orgB")
	assertDone(t, ctx3, false, "u2/orgA")

	// user.banned: u1's remaining (orgB) stream drops.
	if n := r.DropByUser("u1"); n != 1 {
		t.Fatalf("DropByUser dropped %d, want 1", n)
	}
	assertDone(t, ctx2, true, "u1/orgB after ban")
}

// TestConnRegistry_DeregisterRemoves registers a stream and executes the cleanup function returned to its
// handler. A later key revocation must report zero drops because the closed connection is no longer
// tracked. This prevents stale registry entries and repeated cancellation after normal disconnect.
func TestConnRegistry_DeregisterRemoves(t *testing.T) {
	r := NewConnRegistry()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	dereg := r.register(ConnIdentity{KeyID: "key_x"}, cancel)
	dereg()
	if n := r.DropByKey("key_x"); n != 0 {
		t.Fatalf("DropByKey after deregister dropped %d, want 0", n)
	}
}

// TestConnRegistry_DropCancelsOutsideLock uses a cancellation callback that immediately re-enters the
// registry to add another connection. DropByKey must complete and invoke that callback without
// deadlocking, which proves matching entries are removed under the mutex but callbacks run after unlock.
// This is the regression for lock inversion during revocation cleanup.
func TestConnRegistry_DropCancelsOutsideLock(t *testing.T) {
	r := NewConnRegistry()
	called := make(chan struct{})
	r.register(ConnIdentity{KeyID: "key_x"}, func() {
		// Re-entering Register would deadlock if DropByKey held the mutex while
		// invoking caller-provided cancel functions.
		r.register(ConnIdentity{KeyID: "replacement"}, func() {})
		close(called)
	})
	require.Equal(t, 1, r.DropByKey("key_x"))
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("cancel callback deadlocked")
	}
}

func assertDone(t *testing.T, ctx context.Context, want bool, label string) {
	t.Helper()
	select {
	case <-ctx.Done():
		if !want {
			t.Fatalf("%s: context cancelled but should not be", label)
		}
	default:
		if want {
			t.Fatalf("%s: context should be cancelled but is not", label)
		}
	}
}
