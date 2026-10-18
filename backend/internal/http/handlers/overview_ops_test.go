package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/humax"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/service"
)

type fakeOverviewSvc struct {
	org   string
	query service.OverviewQuery
	err   error
}

func (f *fakeOverviewSvc) Get(_ context.Context, org string, q service.OverviewQuery) (apitypes.Overview, error) {
	f.org, f.query = org, q
	if f.err != nil {
		return apitypes.Overview{}, f.err
	}
	return apitypes.Overview{
		Range: q.Range, TZ: q.TZ, From: 1, To: 2, Buckets: []int64{1},
		Series: apitypes.OverviewSeries{
			MessagesReceived: []int64{3}, MessagesSent: []int64{0}, SendsFailed: []int64{0},
			WebhookDeliveries: []int64{0}, WebhookFailures: []int64{0},
		},
		Totals: apitypes.OverviewTotals{MessagesReceived: 3},
	}, nil
}

func overviewRouter(svc *fakeOverviewSvc, perms domain.Permissions) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p := &authz.Principal{Kind: authz.KindAPIKey, OrganizationID: "org_test", KeyPermissions: perms}
			next.ServeHTTP(w, req.WithContext(authz.SetPrincipal(req.Context(), p)))
		})
	})
	RegisterOverviewOps(humax.NewAPI(r), &Handlers{Overview: svc})
	return r
}

func getOverview(t *testing.T, h http.Handler, query string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/overview"+query, nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return w, body
}

func errorCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestGetOverview_DefaultsAndCamelCaseBody(t *testing.T) {
	svc := &fakeOverviewSvc{}
	w, body := getOverview(t, overviewRouter(svc, domain.Permissions{Read: true}), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if svc.org != "org_test" || svc.query != (service.OverviewQuery{Range: "7d", TZ: "UTC"}) {
		t.Fatalf("org=%s query=%+v", svc.org, svc.query)
	}
	for _, key := range []string{"range", "tz", "from", "to", "buckets", "totals", "series", "hasSentMessage", "retentionDays"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("response lacks %q: %s", key, w.Body.String())
		}
	}
	if series := body["series"].(map[string]any); series["messagesReceived"] == nil || series["webhookFailures"] == nil {
		t.Fatalf("series = %v", series)
	}
}

func TestGetOverview_PassesQueryParameters(t *testing.T) {
	svc := &fakeOverviewSvc{}
	w, _ := getOverview(t, overviewRouter(svc, domain.Permissions{Read: true}), "?range=24h&tz=Asia/Kolkata&session=s1")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if svc.query != (service.OverviewQuery{Range: "24h", TZ: "Asia/Kolkata", SessionID: "s1"}) {
		t.Fatalf("query = %+v", svc.query)
	}
}

func TestGetOverview_Errors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		perms  domain.Permissions
		query  string
		svcErr error
		status int
		code   string
	}{
		{"range outside enum", domain.Permissions{Read: true}, "?range=90d", nil, http.StatusBadRequest, domain.CodeValidationError},
		{"service rejects zone", domain.Permissions{Read: true}, "?tz=Nope/Zone", domain.ErrValidation("bad tz"), http.StatusBadRequest, domain.CodeValidationError},
		{"foreign session", domain.Permissions{Read: true}, "?session=x", domain.ErrNotFound("session not found"), http.StatusNotFound, domain.CodeNotFound},
		{"missing read capability", domain.Permissions{Send: true}, "", nil, http.StatusForbidden, domain.CodeForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, body := getOverview(t, overviewRouter(&fakeOverviewSvc{err: tc.svcErr}, tc.perms), tc.query)
			if w.Code != tc.status || errorCode(body) != tc.code {
				t.Fatalf("status=%d code=%s body=%s", w.Code, errorCode(body), w.Body.String())
			}
		})
	}
}
