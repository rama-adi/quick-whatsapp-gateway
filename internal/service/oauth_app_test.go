package service

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

const testPepper = "pepper"

func newOAuthAppServiceTest(t *testing.T) (*OAuthAppService, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(db)
	return NewOAuthAppService(st, testPepper, "am", nil), mock, func() { _ = db.Close() }
}

func expectOAuthSession(mock sqlmock.Sqlmock, id, org string) {
	rows := sqlmock.NewRows([]string{
		"id", "organization_id", "created_by_user_id", "gateway_id", "label", "status",
		"wa_jid", "wa_lid", "phone_number", "is_admin_session", "auto_read", "presence_typing",
		"rate_per_min", "rate_per_hour", "last_connected_at", "created_at", "updated_at",
	}).AddRow(id, org, nil, "gw-1", nil, "working", nil, nil, nil, false, true, false, 20, 200, nil, int64(100), int64(100))
	mock.ExpectQuery("SELECT id, organization_id, created_by_user_id, gateway_id").
		WithArgs(id).WillReturnRows(rows)
}

type secretHashArg struct {
	hash *[]byte
}

func (a secretHashArg) Match(v driver.Value) bool {
	b, ok := v.([]byte)
	if !ok || len(b) != 32 || a.hash == nil {
		return false
	}
	*a.hash = append((*a.hash)[:0], b...)
	return true
}

func TestOAuthAppService_CreateConfidential_ShowsSecretOnceAndHashes(t *testing.T) {
	svc, mock, cleanup := newOAuthAppServiceTest(t)
	defer cleanup()
	expectOAuthSession(mock, "sess_1", "org_1")
	var capturedHash []byte
	hashArg := secretHashArg{hash: &capturedHash}
	mock.ExpectExec("INSERT INTO oauth_clients").
		WithArgs(
			sqlmock.AnyArg(), sqlmock.AnyArg(), "org_1", nil, "sess_1", "Acme", nil,
			"confidential", "login", hashArg, sqlmock.AnyArg(), sqlmock.AnyArg(), "dm",
			nil, sqlmock.AnyArg(), 900, 2592000, "active", sqlmock.AnyArg(), sqlmock.AnyArg(), nil,
		).WillReturnResult(sqlmock.NewResult(0, 1))

	out, err := svc.Create(context.Background(), "org_1", OAuthAppCreateInput{
		SessionID:     "sess_1",
		Name:          "Acme",
		RedirectURIs:  []string{"https://app.example/cb"},
		AllowedScopes: []string{"openid"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	secret := out.ClientSecret
	if secret == "" {
		t.Fatal("expected client secret to be returned on create")
	}
	if !VerifyOAuthClientSecret(secret, testPepper, capturedHash) {
		t.Fatal("stored hash does not verify against returned client secret")
	}
	if out.SecretLast4 == nil || *out.SecretLast4 != secret[len(secret)-4:] {
		t.Fatalf("secretLast4 = %v, secret = %q", out.SecretLast4, secret)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthAppService_CreateValidationMatrix(t *testing.T) {
	tests := []struct {
		name string
		in   OAuthAppCreateInput
	}{
		{name: "https redirect with fragment rejected", in: OAuthAppCreateInput{SessionID: "sess_1", Name: "Acme", RedirectURIs: []string{"https://app.example/cb#frag"}}},
		{name: "non-local http rejected", in: OAuthAppCreateInput{SessionID: "sess_1", Name: "Acme", RedirectURIs: []string{"http://app.example/cb"}}},
		{name: "login command uppercase rejected", in: OAuthAppCreateInput{SessionID: "sess_1", Name: "Acme", LoginCommand: "Login", RedirectURIs: []string{"https://app.example/cb"}}},
		{name: "login command admin prefix rejected", in: OAuthAppCreateInput{SessionID: "sess_1", Name: "Acme", LoginCommand: "am", RedirectURIs: []string{"https://app.example/cb"}}},
		{name: "group mode requires group jid", in: OAuthAppCreateInput{SessionID: "sess_1", Name: "Acme", RedirectURIs: []string{"https://app.example/cb"}, Modes: []string{"group"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mock, cleanup := newOAuthAppServiceTest(t)
			defer cleanup()
			expectOAuthSession(mock, "sess_1", "org_1")
			_, err := svc.Create(context.Background(), "org_1", tt.in)
			if err == nil {
				t.Fatal("expected validation error")
			}
			var apiErr *domain.APIError
			if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
				t.Fatalf("expected API validation error, got %T %v", err, err)
			}
		})
	}
}

func TestOAuthAppService_CrossOrgSessionIsNotFound(t *testing.T) {
	svc, mock, cleanup := newOAuthAppServiceTest(t)
	defer cleanup()
	expectOAuthSession(mock, "sess_1", "org_other")
	_, err := svc.Create(context.Background(), "org_1", OAuthAppCreateInput{
		SessionID:    "sess_1",
		Name:         "Acme",
		RedirectURIs: []string{"https://app.example/cb"},
	})
	if err == nil {
		t.Fatal("expected not_found")
	}
	apiErr, ok := err.(*domain.APIError)
	if !ok || apiErr.Code != domain.CodeNotFound {
		t.Fatalf("expected not_found, got %T %v", err, err)
	}
}

func TestOAuthAppService_DeleteCascades(t *testing.T) {
	svc, mock, cleanup := newOAuthAppServiceTest(t)
	defer cleanup()
	clientRows := sqlmock.NewRows([]string{"id", "client_id", "organization_id", "created_by_user_id", "session_id", "name", "logo_url", "client_type", "login_command", "secret_hash", "secret_last4", "redirect_uris", "modes", "group_jid", "allowed_scopes", "token_ttl_seconds", "refresh_ttl_seconds", "status", "created_at", "updated_at", "deleted_at"}).
		AddRow("oac_1", "wa_1", "org_1", nil, "sess_1", "Acme", nil, "confidential", "login", []byte("hash"), "last", []byte(`["https://app.example/cb"]`), "dm", nil, []byte(`["openid"]`), 900, 2592000, "active", 100, 100, nil)
	mock.ExpectQuery("SELECT .* FROM oauth_clients WHERE organization_id = \\? AND id = \\?").
		WithArgs("org_1", "oac_1").WillReturnRows(clientRows)
	mock.ExpectExec("UPDATE oauth_refresh_tokens rt JOIN oauth_grants g").
		WithArgs(sqlmock.AnyArg(), "org_1", "org_1", "wa_1").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("UPDATE oauth_grants SET revoked_at").
		WithArgs(sqlmock.AnyArg(), "org_1", "wa_1").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("UPDATE oauth_clients SET deleted_at").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "org_1", "oac_1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := svc.Delete(context.Background(), "org_1", "oac_1", false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
