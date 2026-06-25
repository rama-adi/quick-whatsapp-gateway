package auth

// RBAC model the gateway seeds into Authula's generic roles+permissions system.
//
// DISCREPANCY (recon §6): Authula ships NO built-in role names. super_admin/user
// are roles WE define here and seed idempotently (SeedRBAC). The access-control
// enforce hook checks permission KEYS, not role names — so the gate set below is
// the contract every protected route's RouteMapping references.

// Role names (recon §6: created by us, not library constants).
const (
	RoleSuperAdmin = "super_admin" // full administrative access (§10)
	RoleUser       = "user"        // self-service tenant (§10)
)

// Permission keys the gateway gates on. These are the KEYS passed to the
// access-control enforce hook via route metadata / RouteMappings (recon §6).
const (
	PermUsersRead      = "users.read"      // read tenant/user records (admin surfaces)
	PermUsersWrite     = "users.write"     // create/ban/modify tenants (admin surfaces)
	PermSessionsManage = "sessions.manage" // manage WhatsApp sessions cross-tenant
	PermAdminAccess    = "admin.access"    // reach the admin panel / /auth/admin/*
)

// roleSpec is one role plus the permission keys attached to it. Pure data so the
// seed plan can be asserted in a unit test without a DB.
type roleSpec struct {
	Name        string
	Description string
	IsSystem    bool
	Permissions []string
}

// seedPlan is the full, deterministic RBAC seed. SeedRBAC walks this list. The
// "user" role gets no admin permissions — tenant isolation is enforced by the
// app layer via tenant_id, not by an Authula permission. super_admin holds every
// gate key so admin RouteMappings (any of the keys) all resolve to true.
//
// IsSystem MUST be false: Authula's access-control AddPermissionToRole rejects
// attaching a permission to a role (or attaching a system permission) when
// either side has IsSystem=true (services/role_permission_service.go returns
// ErrBadRequest). The "system" flag is reserved for records the library manages
// internally; our app-defined roles are non-system so SeedRBAC can wire their
// permissions through the public API.
func seedPlan() []roleSpec {
	return []roleSpec{
		{
			Name:        RoleSuperAdmin,
			Description: "Full administrative access to all tenants, sessions and admin routes.",
			IsSystem:    false,
			Permissions: allPermissionKeys(),
		},
		{
			Name:        RoleUser,
			Description: "Self-service tenant: manages own sessions, keys, webhooks and events.",
			IsSystem:    false,
			Permissions: nil,
		},
	}
}

// allPermissionKeys is the complete, de-duplicated set of permission keys the
// gateway seeds. Used both for the super_admin grant and to drive permission
// creation in SeedRBAC.
func allPermissionKeys() []string {
	return []string{
		PermUsersRead,
		PermUsersWrite,
		PermSessionsManage,
		PermAdminAccess,
	}
}
