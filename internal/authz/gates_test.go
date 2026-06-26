package authz

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func TestAllow(t *testing.T) {
	keyRead := &Principal{Kind: KindAPIKey, OrganizationID: "o", KeyPermissions: domain.Permissions{Read: true}}
	keyAll := &Principal{Kind: KindAPIKey, OrganizationID: "o", KeyPermissions: domain.Permissions{Read: true, Send: true, Manage: true, Events: true}}
	owner := &Principal{Kind: KindUser, OrganizationID: "o", OrgRole: OrgRoleOwner}
	admin := &Principal{Kind: KindUser, OrganizationID: "o", OrgRole: OrgRoleAdmin}
	member := &Principal{Kind: KindUser, OrganizationID: "o", OrgRole: OrgRoleMember}
	superA := &Principal{Kind: KindUser, OrganizationID: "o", OrgRole: OrgRoleMember, PlatformRole: PlatformRoleSuperAdmin}
	noRole := &Principal{Kind: KindUser, OrganizationID: "o"}

	tests := []struct {
		name string
		p    *Principal
		cap  Capability
		want bool
	}{
		{"nil denied", nil, CapRead, false},
		{"key read allows read", keyRead, CapRead, true},
		{"key read denies send", keyRead, CapSend, false},
		{"key read denies manage", keyRead, CapManage, false},
		{"key all allows manage", keyAll, CapManage, true},
		{"key all allows events", keyAll, CapEvents, true},

		{"owner manages", owner, CapManage, true},
		{"admin manages", admin, CapManage, true},
		{"admin sends", admin, CapSend, true},
		{"member reads", member, CapRead, true},
		{"member sends", member, CapSend, true},
		{"member events", member, CapEvents, true},
		{"member cannot manage", member, CapManage, false},
		{"unknown org role denied", noRole, CapRead, false},

		{"super_admin manages across orgs", superA, CapManage, true},
		{"super_admin reads", superA, CapRead, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Allow(tt.p, tt.cap); got != tt.want {
				t.Errorf("Allow(%+v, %s) = %v, want %v", tt.p, tt.cap, got, tt.want)
			}
		})
	}
}

func gateNext() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestRequire(t *testing.T) {
	member := &Principal{Kind: KindUser, OrganizationID: "o", OrgRole: OrgRoleMember}

	tests := []struct {
		name       string
		principal  *Principal
		gate       func() func(http.Handler) http.Handler
		wantStatus int
		wantCode   string
	}{
		{
			name: "no principal -> 401", principal: nil, gate: RequireRead,
			wantStatus: http.StatusUnauthorized, wantCode: domain.CodeUnauthorized,
		},
		{
			name: "member read allowed", principal: member, gate: RequireRead,
			wantStatus: http.StatusOK,
		},
		{
			name: "member manage forbidden -> 403", principal: member, gate: RequireManage,
			wantStatus: http.StatusForbidden, wantCode: domain.CodeForbidden,
		},
		{
			name: "member is not super_admin -> 403", principal: member, gate: RequireSuperAdmin,
			wantStatus: http.StatusForbidden, wantCode: domain.CodeForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := tt.gate()(gateNext())
			r := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
			if tt.principal != nil {
				r = r.WithContext(SetPrincipal(r.Context(), tt.principal))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantCode != "" {
				if got := decodeErrCode(t, rec); got != tt.wantCode {
					t.Errorf("error code = %q, want %q", got, tt.wantCode)
				}
			}
		})
	}
}

func TestSetPrincipalMirrorsOrg(t *testing.T) {
	p := &Principal{Kind: KindUser, OrganizationID: "org_mirror"}
	ctx := SetPrincipal(t.Context(), p)
	if got := FromContext(ctx); got == nil || got.OrganizationID != "org_mirror" {
		t.Fatalf("FromContext = %+v, want org_mirror", got)
	}
}
