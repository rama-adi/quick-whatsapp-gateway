package auth

import (
	"context"
	"fmt"
)

// SeedRBAC idempotently creates the gateway's roles (super_admin, user) and the
// permission keys it gates on (users.read/write, sessions.manage, admin.access),
// then attaches the right permissions to each role.
//
// Idempotency strategy (recon §6): reconcile against the live state via the
// "get all" reads (which never error on absence) instead of relying on
// not-found sentinels. Re-running SeedRBAC is a no-op once everything exists.
func SeedRBAC(ctx context.Context, store RBACStore) error {
	// 1. Ensure every permission key exists; build key -> id map.
	permByKey, err := ensurePermissions(ctx, store, allPermissionKeys())
	if err != nil {
		return fmt.Errorf("auth: seed permissions: %w", err)
	}

	// 2. Ensure every role exists; build name -> Role map.
	roleByName, err := ensureRoles(ctx, store, seedPlan())
	if err != nil {
		return fmt.Errorf("auth: seed roles: %w", err)
	}

	// 3. Attach each role's permissions (skip ones already attached).
	for _, spec := range seedPlan() {
		role := roleByName[spec.Name]
		existing, err := store.GetRolePermissionKeys(ctx, role.ID)
		if err != nil {
			return fmt.Errorf("auth: read role %q permissions: %w", spec.Name, err)
		}
		have := toSet(existing)
		for _, key := range spec.Permissions {
			if have[key] {
				continue
			}
			perm, ok := permByKey[key]
			if !ok {
				return fmt.Errorf("auth: role %q wants unknown permission %q", spec.Name, key)
			}
			if err := store.AddPermissionToRole(ctx, role.ID, perm.ID); err != nil {
				return fmt.Errorf("auth: attach %q to %q: %w", key, spec.Name, err)
			}
		}
	}
	return nil
}

// ensurePermissions creates any missing permission keys and returns key -> Permission.
func ensurePermissions(ctx context.Context, store RBACStore, keys []string) (map[string]Permission, error) {
	current, err := store.GetAllPermissions(ctx)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]Permission, len(current))
	for _, p := range current {
		byKey[p.Key] = p
	}
	for _, key := range keys {
		if _, ok := byKey[key]; ok {
			continue
		}
		// isSystem=false: a system permission cannot be attached to a role via
		// Authula's AddPermissionToRole (it returns ErrBadRequest). See seedPlan.
		p, err := store.CreatePermission(ctx, key, permissionDescription(key), false)
		if err != nil {
			return nil, fmt.Errorf("create permission %q: %w", key, err)
		}
		byKey[key] = p
	}
	return byKey, nil
}

// ensureRoles creates any missing roles and returns name -> Role.
func ensureRoles(ctx context.Context, store RBACStore, specs []roleSpec) (map[string]Role, error) {
	current, err := store.GetAllRoles(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]Role, len(current))
	for _, r := range current {
		byName[r.Name] = r
	}
	for _, spec := range specs {
		if _, ok := byName[spec.Name]; ok {
			continue
		}
		r, err := store.CreateRole(ctx, spec.Name, spec.Description, spec.IsSystem)
		if err != nil {
			return nil, fmt.Errorf("create role %q: %w", spec.Name, err)
		}
		byName[spec.Name] = r
	}
	return byName, nil
}

// permissionDescription gives each gated key a human-readable description for the
// admin UI. Falls back to the key itself for unknown keys.
func permissionDescription(key string) string {
	switch key {
	case PermUsersRead:
		return "Read tenant and user records."
	case PermUsersWrite:
		return "Create, modify and ban tenants/users."
	case PermSessionsManage:
		return "Manage WhatsApp sessions across tenants."
	case PermAdminAccess:
		return "Access the administrative panel."
	default:
		return key
	}
}

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, it := range items {
		s[it] = true
	}
	return s
}
