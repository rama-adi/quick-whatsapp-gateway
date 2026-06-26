package stream

import (
	"context"
	"testing"
)

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
