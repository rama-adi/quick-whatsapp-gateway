package auth

import (
	"context"
	"fmt"
	"strings"
)

// adminDisplayName is the name given to the bootstrapped super-admin user.
const adminDisplayName = "Administrator"

// BootstrapAdmin idempotently provisions the super-admin from ADMIN_EMAIL /
// ADMIN_PASSWORD (§10). If a user with the email already exists it ensures the
// super_admin role is assigned (no sign-up); otherwise it signs the user up and
// assigns the role. Empty email/password is treated as "no admin configured" and
// is a no-op so a deployment can opt out.
//
// SeedRBAC MUST have run first so the super_admin role exists.
func BootstrapAdmin(ctx context.Context, dir UserDirectory, signup SignUpStore, rbac RBACStore, email, password string) error {
	email = strings.TrimSpace(email)
	if email == "" || password == "" {
		// Nothing to bootstrap; let the caller log this decision.
		return nil
	}

	existing, err := dir.GetByEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("auth: lookup admin %q: %w", email, err)
	}

	var userID string
	if existing != nil {
		userID = existing.ID
	} else {
		userID, err = signup.SignUp(ctx, adminDisplayName, email, password)
		if err != nil {
			return fmt.Errorf("auth: sign up admin %q: %w", email, err)
		}
	}

	return ensureSuperAdminRole(ctx, rbac, userID)
}

// ensureSuperAdminRole assigns the super_admin role to userID unless already held.
func ensureSuperAdminRole(ctx context.Context, rbac RBACStore, userID string) error {
	roleNames, err := rbac.GetUserRoleNames(ctx, userID)
	if err != nil {
		return fmt.Errorf("auth: read roles for %q: %w", userID, err)
	}
	for _, n := range roleNames {
		if n == RoleSuperAdmin {
			return nil // already a super-admin; idempotent no-op
		}
	}

	roles, err := rbac.GetAllRoles(ctx)
	if err != nil {
		return fmt.Errorf("auth: read roles: %w", err)
	}
	var roleID string
	for _, r := range roles {
		if r.Name == RoleSuperAdmin {
			roleID = r.ID
			break
		}
	}
	if roleID == "" {
		return fmt.Errorf("auth: role %q not seeded; run SeedRBAC before BootstrapAdmin", RoleSuperAdmin)
	}
	if err := rbac.AssignRoleToUser(ctx, userID, roleID); err != nil {
		return fmt.Errorf("auth: assign %q to %q: %w", RoleSuperAdmin, userID, err)
	}
	return nil
}
