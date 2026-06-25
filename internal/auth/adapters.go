package auth

import (
	"context"

	accesscontrol "github.com/Authula/authula/plugins/access-control"
	actypes "github.com/Authula/authula/plugins/access-control/types"
	emailpassword "github.com/Authula/authula/plugins/email-password"
	coreservices "github.com/Authula/authula/services"
)

// Adapters map the live Authula plugin/core APIs onto the consumer-defined ports.
// They are thin (no logic) so the testable logic stays in seed.go/bootstrap.go.

// --- RBAC adapter over accesscontrol.API ---

type rbacAdapter struct{ api *accesscontrol.API }

func newRBACAdapter(api *accesscontrol.API) RBACStore { return &rbacAdapter{api: api} }

func (a *rbacAdapter) GetAllRoles(ctx context.Context) ([]Role, error) {
	roles, err := a.api.GetAllRoles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Role, 0, len(roles))
	for _, r := range roles {
		out = append(out, Role{ID: r.ID, Name: r.Name})
	}
	return out, nil
}

func (a *rbacAdapter) CreateRole(ctx context.Context, name, description string, isSystem bool) (Role, error) {
	desc := description
	r, err := a.api.CreateRole(ctx, actypes.CreateRoleRequest{
		Name:        name,
		Description: &desc,
		IsSystem:    isSystem,
	})
	if err != nil {
		return Role{}, err
	}
	return Role{ID: r.ID, Name: r.Name}, nil
}

func (a *rbacAdapter) GetAllPermissions(ctx context.Context) ([]Permission, error) {
	perms, err := a.api.GetAllPermissions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Permission, 0, len(perms))
	for _, p := range perms {
		out = append(out, Permission{ID: p.ID, Key: p.Key})
	}
	return out, nil
}

func (a *rbacAdapter) CreatePermission(ctx context.Context, key, description string, isSystem bool) (Permission, error) {
	desc := description
	p, err := a.api.CreatePermission(ctx, actypes.CreatePermissionRequest{
		Key:         key,
		Description: &desc,
		IsSystem:    isSystem,
	})
	if err != nil {
		return Permission{}, err
	}
	return Permission{ID: p.ID, Key: p.Key}, nil
}

func (a *rbacAdapter) GetRolePermissionKeys(ctx context.Context, roleID string) ([]string, error) {
	perms, err := a.api.GetRolePermissions(ctx, roleID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(perms))
	for _, p := range perms {
		keys = append(keys, p.PermissionKey)
	}
	return keys, nil
}

func (a *rbacAdapter) AddPermissionToRole(ctx context.Context, roleID, permissionID string) error {
	return a.api.AddPermissionToRole(ctx, roleID, permissionID, nil)
}

func (a *rbacAdapter) GetUserRoleNames(ctx context.Context, userID string) ([]string, error) {
	roles, err := a.api.GetUserRoles(ctx, userID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.RoleName)
	}
	return names, nil
}

func (a *rbacAdapter) AssignRoleToUser(ctx context.Context, userID, roleID string) error {
	return a.api.AssignRoleToUser(ctx, userID, actypes.AssignUserRoleRequest{RoleID: roleID}, nil)
}

func (a *rbacAdapter) UserHasPermission(ctx context.Context, userID, permKey string) (bool, error) {
	return a.api.HasPermissions(ctx, userID, []string{permKey})
}

// --- UserDirectory adapter over CoreServices.UserService ---

type userDirectory struct{ cs *coreservices.CoreServices }

func newUserDirectory(cs *coreservices.CoreServices) UserDirectory { return &userDirectory{cs: cs} }

func (d *userDirectory) GetByEmail(ctx context.Context, email string) (*User, error) {
	u, err := d.cs.UserService.GetByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if u == nil { // recon §5: nil,nil when not found
		return nil, nil
	}
	return &User{ID: u.ID, Email: u.Email}, nil
}

// --- SignUpStore adapter over email_password.API ---

type signUpAdapter struct{ api *emailpassword.API }

func newSignUpAdapter(api *emailpassword.API) SignUpStore { return &signUpAdapter{api: api} }

func (s *signUpAdapter) SignUp(ctx context.Context, name, email, password string) (string, error) {
	res, err := s.api.SignUp(ctx, name, email, password, nil, nil, nil, nil, nil)
	if err != nil {
		return "", err
	}
	return res.User.ID, nil
}
