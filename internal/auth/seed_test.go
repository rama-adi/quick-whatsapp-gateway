package auth

import (
	"context"
	"sort"
	"testing"
)

// TestSeedPlanKeySet locks the RBAC role/permission KEY SET (the contract every
// protected route references). A change here is a deliberate behavior change.
func TestSeedPlanKeySet(t *testing.T) {
	gotPerms := append([]string(nil), allPermissionKeys()...)
	sort.Strings(gotPerms)
	wantPerms := []string{"admin.access", "sessions.manage", "users.read", "users.write"}
	if len(gotPerms) != len(wantPerms) {
		t.Fatalf("permission keys = %v, want %v", gotPerms, wantPerms)
	}
	for i := range wantPerms {
		if gotPerms[i] != wantPerms[i] {
			t.Fatalf("permission keys = %v, want %v", gotPerms, wantPerms)
		}
	}

	plan := seedPlan()
	if len(plan) != 2 {
		t.Fatalf("want 2 roles, got %d", len(plan))
	}
	byName := map[string]roleSpec{}
	for _, r := range plan {
		byName[r.Name] = r
	}
	sa, ok := byName[RoleSuperAdmin]
	if !ok {
		t.Fatal("super_admin role missing from seed plan")
	}
	if len(sa.Permissions) != len(allPermissionKeys()) {
		t.Fatalf("super_admin should hold every permission, got %v", sa.Permissions)
	}
	u, ok := byName[RoleUser]
	if !ok {
		t.Fatal("user role missing from seed plan")
	}
	if len(u.Permissions) != 0 {
		t.Fatalf("user role should hold no admin permissions, got %v", u.Permissions)
	}
}

// TestSeedRBACCreatesEverythingOnce verifies a clean seed creates all roles,
// permissions and attachments exactly once.
func TestSeedRBACCreatesEverythingOnce(t *testing.T) {
	f := newFakeRBAC()
	if err := SeedRBAC(context.Background(), f); err != nil {
		t.Fatalf("SeedRBAC: %v", err)
	}
	if f.createRoles != 2 {
		t.Errorf("created %d roles, want 2", f.createRoles)
	}
	if f.createPerms != len(allPermissionKeys()) {
		t.Errorf("created %d perms, want %d", f.createPerms, len(allPermissionKeys()))
	}
	// super_admin must end up with all permissions attached.
	sa := f.roles[RoleSuperAdmin]
	if got := len(f.rolePerms[sa.ID]); got != len(allPermissionKeys()) {
		t.Errorf("super_admin has %d perms, want %d", got, len(allPermissionKeys()))
	}
	// user must have none.
	u := f.roles[RoleUser]
	if got := len(f.rolePerms[u.ID]); got != 0 {
		t.Errorf("user has %d perms, want 0", got)
	}
}

// TestSeedRBACIdempotent verifies re-running creates/attaches nothing new.
func TestSeedRBACIdempotent(t *testing.T) {
	f := newFakeRBAC()
	ctx := context.Background()
	if err := SeedRBAC(ctx, f); err != nil {
		t.Fatalf("first SeedRBAC: %v", err)
	}
	rolesAfterFirst, permsAfterFirst, attachAfterFirst := f.createRoles, f.createPerms, f.attachCalls

	if err := SeedRBAC(ctx, f); err != nil {
		t.Fatalf("second SeedRBAC: %v", err)
	}
	if f.createRoles != rolesAfterFirst {
		t.Errorf("re-seed created %d extra roles", f.createRoles-rolesAfterFirst)
	}
	if f.createPerms != permsAfterFirst {
		t.Errorf("re-seed created %d extra perms", f.createPerms-permsAfterFirst)
	}
	if f.attachCalls != attachAfterFirst {
		t.Errorf("re-seed made %d extra attach calls", f.attachCalls-attachAfterFirst)
	}
}

// TestSeedRBACPropagatesErrors checks errors are wrapped, not swallowed.
func TestSeedRBACPropagatesErrors(t *testing.T) {
	for _, method := range []string{"CreatePermission", "CreateRole", "AddPermissionToRole"} {
		t.Run(method, func(t *testing.T) {
			f := newFakeRBAC()
			f.failOn = method
			if err := SeedRBAC(context.Background(), f); err == nil {
				t.Fatalf("expected error when %s fails", method)
			}
		})
	}
}
