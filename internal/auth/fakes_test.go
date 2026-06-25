package auth

import (
	"context"
	"errors"
)

// fakeRBAC is an in-memory RBACStore for testing SeedRBAC / BootstrapAdmin /
// the Require* guards. It records calls so idempotency can be asserted.
type fakeRBAC struct {
	roles       map[string]Role            // name -> role
	perms       map[string]Permission      // key -> permission
	rolePerms   map[string]map[string]bool // roleID -> set of permission keys
	userRoles   map[string]map[string]bool // userID -> set of role names (by id->name lookup)
	nextID      int
	createRoles int
	createPerms int
	attachCalls int
	assignCalls int
	failOn      string // method name to force an error on
}

func newFakeRBAC() *fakeRBAC {
	return &fakeRBAC{
		roles:     map[string]Role{},
		perms:     map[string]Permission{},
		rolePerms: map[string]map[string]bool{},
		userRoles: map[string]map[string]bool{},
	}
}

func (f *fakeRBAC) id(prefix string) string {
	f.nextID++
	return prefix + string(rune('A'+f.nextID))
}

func (f *fakeRBAC) GetAllRoles(ctx context.Context) ([]Role, error) {
	if f.failOn == "GetAllRoles" {
		return nil, errors.New("boom")
	}
	out := make([]Role, 0, len(f.roles))
	for _, r := range f.roles {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeRBAC) CreateRole(ctx context.Context, name, description string, isSystem bool) (Role, error) {
	if f.failOn == "CreateRole" {
		return Role{}, errors.New("boom")
	}
	f.createRoles++
	r := Role{ID: f.id("role_"), Name: name}
	f.roles[name] = r
	f.rolePerms[r.ID] = map[string]bool{}
	return r, nil
}

func (f *fakeRBAC) GetAllPermissions(ctx context.Context) ([]Permission, error) {
	out := make([]Permission, 0, len(f.perms))
	for _, p := range f.perms {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeRBAC) CreatePermission(ctx context.Context, key, description string, isSystem bool) (Permission, error) {
	if f.failOn == "CreatePermission" {
		return Permission{}, errors.New("boom")
	}
	f.createPerms++
	p := Permission{ID: f.id("perm_"), Key: key}
	f.perms[key] = p
	return p, nil
}

func (f *fakeRBAC) GetRolePermissionKeys(ctx context.Context, roleID string) ([]string, error) {
	set := f.rolePerms[roleID]
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	return keys, nil
}

func (f *fakeRBAC) AddPermissionToRole(ctx context.Context, roleID, permissionID string) error {
	if f.failOn == "AddPermissionToRole" {
		return errors.New("boom")
	}
	f.attachCalls++
	// resolve permission key from id
	var key string
	for k, p := range f.perms {
		if p.ID == permissionID {
			key = k
			break
		}
	}
	if f.rolePerms[roleID] == nil {
		f.rolePerms[roleID] = map[string]bool{}
	}
	f.rolePerms[roleID][key] = true
	return nil
}

func (f *fakeRBAC) GetUserRoleNames(ctx context.Context, userID string) ([]string, error) {
	if f.failOn == "GetUserRoleNames" {
		return nil, errors.New("boom")
	}
	set := f.userRoles[userID]
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	return names, nil
}

func (f *fakeRBAC) AssignRoleToUser(ctx context.Context, userID, roleID string) error {
	if f.failOn == "AssignRoleToUser" {
		return errors.New("boom")
	}
	f.assignCalls++
	var name string
	for n, r := range f.roles {
		if r.ID == roleID {
			name = n
			break
		}
	}
	if f.userRoles[userID] == nil {
		f.userRoles[userID] = map[string]bool{}
	}
	f.userRoles[userID][name] = true
	return nil
}

func (f *fakeRBAC) UserHasPermission(ctx context.Context, userID, permKey string) (bool, error) {
	if f.failOn == "UserHasPermission" {
		return false, errors.New("boom")
	}
	for roleName := range f.userRoles[userID] {
		r := f.roles[roleName]
		if f.rolePerms[r.ID][permKey] {
			return true, nil
		}
	}
	return false, nil
}

// fakeDirectory implements UserDirectory.
type fakeDirectory struct {
	byEmail map[string]*User
	err     error
}

func (d *fakeDirectory) GetByEmail(ctx context.Context, email string) (*User, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.byEmail[email], nil // nil,nil when absent (recon §5)
}

// fakeSignUp implements SignUpStore.
type fakeSignUp struct {
	calls  int
	nextID string
	err    error
}

func (s *fakeSignUp) SignUp(ctx context.Context, name, email, password string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.calls++
	if s.nextID == "" {
		s.nextID = "user_new"
	}
	return s.nextID, nil
}

// fakeTenantStore implements TenantStore.
type fakeTenantStore struct {
	calls   int
	lastID  string
	lastEml string
	lastNow int64
	err     error
}

func (t *fakeTenantStore) UpsertTenant(ctx context.Context, id, email string, now int64) error {
	if t.err != nil {
		return t.err
	}
	t.calls++
	t.lastID, t.lastEml, t.lastNow = id, email, now
	return nil
}

// fixedClock implements Clock.
type fixedClock struct{ ms int64 }

func (c fixedClock) NowMs() int64 { return c.ms }
