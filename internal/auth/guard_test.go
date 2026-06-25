package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newGuardAuth builds an *Auth wired only with an RBAC store and a fixed tenant
// resolver — enough to test RequireRole/RequirePermission without authula.New.
func newGuardAuth(rbac RBACStore, tenantID string, authed bool) *Auth {
	return &Auth{
		rbac: rbac,
		resolveTenantID: func(*http.Request) (string, bool) {
			return tenantID, authed
		},
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestRequireRole(t *testing.T) {
	f := seededRBAC(t)
	// give user_admin the super_admin role
	sa := f.roles[RoleSuperAdmin]
	_ = f.AssignRoleToUser(context.Background(), "user_admin", sa.ID)

	tests := []struct {
		name       string
		tenantID   string
		authed     bool
		role       string
		wantStatus int
	}{
		{"unauthenticated -> 401", "", false, RoleSuperAdmin, http.StatusUnauthorized},
		{"has role -> 200", "user_admin", true, RoleSuperAdmin, http.StatusOK},
		{"missing role -> 403", "user_admin", true, RoleUser, http.StatusForbidden},
		{"unknown user -> 403", "user_ghost", true, RoleSuperAdmin, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newGuardAuth(f, tt.tenantID, tt.authed)
			h := a.RequireRole(tt.role)(okHandler())
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestRequirePermission(t *testing.T) {
	f := seededRBAC(t)
	sa := f.roles[RoleSuperAdmin]
	_ = f.AssignRoleToUser(context.Background(), "user_admin", sa.ID)
	u := f.roles[RoleUser]
	_ = f.AssignRoleToUser(context.Background(), "user_plain", u.ID)

	tests := []struct {
		name       string
		tenantID   string
		authed     bool
		perm       string
		wantStatus int
	}{
		{"unauthenticated -> 401", "", false, PermAdminAccess, http.StatusUnauthorized},
		{"super_admin has admin.access -> 200", "user_admin", true, PermAdminAccess, http.StatusOK},
		{"plain user lacks admin.access -> 403", "user_plain", true, PermAdminAccess, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newGuardAuth(f, tt.tenantID, tt.authed)
			h := a.RequirePermission(tt.perm)(okHandler())
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestGuardErrorBody verifies the §11 error envelope shape on a denial.
func TestGuardErrorBody(t *testing.T) {
	f := seededRBAC(t)
	a := newGuardAuth(f, "", false)
	h := a.RequireRole(RoleSuperAdmin)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	body := rec.Body.String()
	// structural check for the {"error":{"code":"unauthorized",...}} envelope (§11)
	if !strings.Contains(body, `"code":"unauthorized"`) {
		t.Fatalf("body missing unauthorized code: %s", body)
	}
}
