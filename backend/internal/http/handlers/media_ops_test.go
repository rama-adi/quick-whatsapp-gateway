package handlers

import (
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/crypto"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/humax"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/media"
	"net/http"
	"strings"
	"testing"
)

func mediaRouter(s *media.Service, p *authz.Principal) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if p != nil {
				req = req.WithContext(authz.SetPrincipal(req.Context(), p))
			}
			next.ServeHTTP(w, req)
		})
	})
	api := humax.NewAPI(r)
	RegisterMediaOps(api, &Handlers{Media: s})
	RegisterMediaContentOps(api, &Handlers{Media: s})
	return r
}
func TestStorageHTTPPermissionsAndSecretResponses(t *testing.T) {
	body := `{"name":"Archive","endpoint":"https://s3.example.com","region":"us-east-1","bucket":"files","pathStyle":true,"retentionDays":null,"accessKey":"test-access-secret","secretKey":"test-private-secret"}`
	denied := doReq(mediaRouter(nil, &authz.Principal{Kind: authz.KindUser, OrganizationID: testOrganization, OrgRole: authz.OrgRoleMember}), "POST", "/api/v1/storage/buckets", body)
	if denied.Code != 403 {
		t.Fatalf("member create status=%d %s", denied.Code, denied.Body.String())
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, _ := crypto.NewAESGCMFromKey(make([]byte, 32))
	service := &media.Service{DB: db, Cipher: cipher}
	mock.ExpectExec("INSERT INTO media_buckets").WillReturnResult(sqlmock.NewResult(1, 1))
	response := doReq(mediaRouter(service, manageOrgPrincipal()), "POST", "/api/v1/storage/buckets", body)
	if response.Code != 200 {
		t.Fatalf("create status=%d %s", response.Code, response.Body.String())
	}
	for _, secret := range []string{"test-access-secret", "test-private-secret", "accessKey", "secretKey"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatal("credential exposed", secret)
		}
	}
	mock.ExpectQuery("SELECT .* FROM media_assets WHERE id=\\? AND organization_id=\\?").WithArgs("asset", testOrganization).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	response = doReq(mediaRouter(service, manageOrgPrincipal()), "GET", "/api/v1/media/asset", "")
	if response.Code != 404 {
		t.Fatalf("foreign asset status=%d %s", response.Code, response.Body.String())
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
