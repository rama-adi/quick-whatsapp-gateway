package auth

import (
	"context"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// Consumer-defined ports. internal/auth depends on these small interfaces rather
// than on Authula's concrete plugin APIs, so SeedRBAC / BootstrapAdmin / SyncTenant
// are unit-testable with fakes (Go convention: interfaces defined by the consumer).
// Build adapts the real Authula instances to these (see adapters.go).

// Role / Permission / SignUp are trimmed views of the Authula return types,
// carrying only the fields this package needs. Adapters map the real structs in.

// Role is a seeded RBAC role.
type Role struct {
	ID   string
	Name string
}

// Permission is a seeded RBAC permission keyed by Key.
type Permission struct {
	ID  string
	Key string
}

// User is the minimal view of an Authula user this package consumes.
type User struct {
	ID    string
	Email string
}

// RBACStore is the slice of Authula's access-control API the gateway drives to
// seed roles/permissions and assign roles. All calls are idempotent-friendly:
// the "get all" variants never error on absence (recon §6), so SeedRBAC reconciles
// against the current state instead of relying on not-found sentinels.
type RBACStore interface {
	GetAllRoles(ctx context.Context) ([]Role, error)
	CreateRole(ctx context.Context, name, description string, isSystem bool) (Role, error)

	GetAllPermissions(ctx context.Context) ([]Permission, error)
	CreatePermission(ctx context.Context, key, description string, isSystem bool) (Permission, error)

	// GetRolePermissionKeys returns the permission keys currently attached to a role.
	GetRolePermissionKeys(ctx context.Context, roleID string) ([]string, error)
	// AddPermissionToRole attaches a permission to a role (idempotent caller-side).
	AddPermissionToRole(ctx context.Context, roleID, permissionID string) error

	// GetUserRoleNames returns the role names currently assigned to a user.
	GetUserRoleNames(ctx context.Context, userID string) ([]string, error)
	// AssignRoleToUser grants a role to a user.
	AssignRoleToUser(ctx context.Context, userID, roleID string) error

	// UserHasPermission reports whether the user holds the given permission key
	// through any of its roles (backs RequirePermission).
	UserHasPermission(ctx context.Context, userID, permKey string) (bool, error)
}

// UserDirectory looks up Authula users for idempotent admin bootstrap.
type UserDirectory interface {
	// GetByEmail returns (nil, nil) when no user has the email (recon §5/§7).
	GetByEmail(ctx context.Context, email string) (*User, error)
}

// SignUpStore creates a user+account with a hashed password (email-password plugin).
type SignUpStore interface {
	// SignUp creates the credential and returns the new user id.
	SignUp(ctx context.Context, name, email, password string) (userID string, err error)
}

// TenantStore is the app-side tenants mirror (§5). Defined HERE, not imported
// from internal/store, per the parallel-safe import rule. Phase 3 wires the
// concrete MySQL repo in. Upsert is keyed by the Authula user id (tenants.id).
type TenantStore interface {
	// UpsertTenant inserts-or-updates the tenants row for an Authula user.
	UpsertTenant(ctx context.Context, id, email string, now int64) error
}

// Clock is injected so SyncTenant timestamps are deterministic in tests.
type Clock interface{ NowMs() int64 }

// wallClock is the production Clock backed by domain.NowMs (epoch-ms, §5).
type wallClock struct{}

func (wallClock) NowMs() int64 { return domain.NowMs() }
