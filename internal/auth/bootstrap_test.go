package auth

import (
	"context"
	"errors"
	"testing"
)

// seededRBAC returns a fakeRBAC with the gateway roles already seeded.
func seededRBAC(t *testing.T) *fakeRBAC {
	t.Helper()
	f := newFakeRBAC()
	if err := SeedRBAC(context.Background(), f); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return f
}

// TestBootstrapAdminNewUser signs up a fresh admin and assigns super_admin.
func TestBootstrapAdminNewUser(t *testing.T) {
	f := seededRBAC(t)
	dir := &fakeDirectory{byEmail: map[string]*User{}}
	su := &fakeSignUp{nextID: "user_admin"}

	err := BootstrapAdmin(context.Background(), dir, su, f, "admin@example.com", "pw")
	if err != nil {
		t.Fatalf("BootstrapAdmin: %v", err)
	}
	if su.calls != 1 {
		t.Errorf("SignUp calls = %d, want 1", su.calls)
	}
	if !f.userRoles["user_admin"][RoleSuperAdmin] {
		t.Error("admin not assigned super_admin")
	}
}

// TestBootstrapAdminExistingUser does NOT sign up, only ensures the role.
func TestBootstrapAdminExistingUser(t *testing.T) {
	f := seededRBAC(t)
	dir := &fakeDirectory{byEmail: map[string]*User{
		"admin@example.com": {ID: "user_existing", Email: "admin@example.com"},
	}}
	su := &fakeSignUp{}

	if err := BootstrapAdmin(context.Background(), dir, su, f, "admin@example.com", "pw"); err != nil {
		t.Fatalf("BootstrapAdmin: %v", err)
	}
	if su.calls != 0 {
		t.Errorf("SignUp must not be called for existing user, got %d calls", su.calls)
	}
	if !f.userRoles["user_existing"][RoleSuperAdmin] {
		t.Error("existing admin not assigned super_admin")
	}
}

// TestBootstrapAdminIdempotent: second run is a no-op (no extra assign/sign-up).
func TestBootstrapAdminIdempotent(t *testing.T) {
	f := seededRBAC(t)
	dir := &fakeDirectory{byEmail: map[string]*User{
		"admin@example.com": {ID: "user_existing", Email: "admin@example.com"},
	}}
	su := &fakeSignUp{}
	ctx := context.Background()

	if err := BootstrapAdmin(ctx, dir, su, f, "admin@example.com", "pw"); err != nil {
		t.Fatalf("first: %v", err)
	}
	assignsAfterFirst := f.assignCalls
	if err := BootstrapAdmin(ctx, dir, su, f, "admin@example.com", "pw"); err != nil {
		t.Fatalf("second: %v", err)
	}
	if f.assignCalls != assignsAfterFirst {
		t.Errorf("re-run made %d extra assign calls", f.assignCalls-assignsAfterFirst)
	}
}

// TestBootstrapAdminNoOpWhenUnconfigured: empty creds -> nothing happens.
func TestBootstrapAdminNoOpWhenUnconfigured(t *testing.T) {
	f := seededRBAC(t)
	dir := &fakeDirectory{byEmail: map[string]*User{}}
	su := &fakeSignUp{}

	cases := []struct{ email, pw string }{
		{"", "pw"},
		{"admin@example.com", ""},
		{"  ", "pw"},
	}
	for _, c := range cases {
		if err := BootstrapAdmin(context.Background(), dir, su, f, c.email, c.pw); err != nil {
			t.Fatalf("expected no-op, got %v", err)
		}
	}
	if su.calls != 0 || f.assignCalls != 0 {
		t.Errorf("unconfigured bootstrap mutated state: signups=%d assigns=%d", su.calls, f.assignCalls)
	}
}

// TestBootstrapAdminMissingRole errors clearly if SeedRBAC was skipped.
func TestBootstrapAdminMissingRole(t *testing.T) {
	f := newFakeRBAC() // NOT seeded
	dir := &fakeDirectory{byEmail: map[string]*User{}}
	su := &fakeSignUp{nextID: "user_admin"}

	err := BootstrapAdmin(context.Background(), dir, su, f, "admin@example.com", "pw")
	if err == nil {
		t.Fatal("expected error when super_admin role not seeded")
	}
}

// TestBootstrapAdminLookupError surfaces directory failures.
func TestBootstrapAdminLookupError(t *testing.T) {
	f := seededRBAC(t)
	dir := &fakeDirectory{err: errors.New("db down")}
	su := &fakeSignUp{}

	if err := BootstrapAdmin(context.Background(), dir, su, f, "admin@example.com", "pw"); err == nil {
		t.Fatal("expected lookup error to propagate")
	}
}
