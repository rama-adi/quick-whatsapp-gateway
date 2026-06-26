package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func newSessionHandlers(svc SessionSvc) *Handlers {
	return &Handlers{Sessions: svc}
}

func TestCreateSession_HappyPath(t *testing.T) {
	svc := &fakeSessionSvc{created: domain.WASession{ID: "sess_1", OrganizationID: testOrganization, Status: domain.SessionStopped}}
	h := newSessionHandlers(svc)

	r := withOrganization(chiReq(http.MethodPost, "/api/v1/sessions", `{"label":"work","autoRead":false}`, nil), testOrganization)
	w := httptest.NewRecorder()
	h.CreateSession(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var got domain.WASession
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "sess_1" {
		t.Errorf("id = %q, want sess_1", got.ID)
	}
	if svc.createIn.AutoRead == nil || *svc.createIn.AutoRead != false {
		t.Errorf("autoRead not threaded to service: %+v", svc.createIn)
	}
}

func TestCreateSession_NoOrganization401(t *testing.T) {
	h := newSessionHandlers(&fakeSessionSvc{})
	r := chiReq(http.MethodPost, "/api/v1/sessions", `{}`, nil) // no organization on ctx
	w := httptest.NewRecorder()
	h.CreateSession(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeUnauthorized {
		t.Errorf("code = %q, want %q", got, domain.CodeUnauthorized)
	}
}

func TestCreateSession_BadJSON(t *testing.T) {
	h := newSessionHandlers(&fakeSessionSvc{})
	r := withOrganization(chiReq(http.MethodPost, "/api/v1/sessions", `{"label":`, nil), testOrganization)
	w := httptest.NewRecorder()
	h.CreateSession(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := decodeError(w.Body.String()).Error.Code; got != domain.CodeValidationError {
		t.Errorf("code = %q, want %q", got, domain.CodeValidationError)
	}
}

func TestListSessions_Envelope(t *testing.T) {
	svc := &fakeSessionSvc{list: []domain.WASession{{ID: "sess_1"}, {ID: "sess_2"}}}
	h := newSessionHandlers(svc)
	r := withOrganization(chiReq(http.MethodGet, "/api/v1/sessions", "", nil), testOrganization)
	w := httptest.NewRecorder()
	h.ListSessions(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var env struct {
		Data []domain.WASession `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 2 {
		t.Errorf("data len = %d, want 2", len(env.Data))
	}
}

func TestStartSession_ReturnsRefreshedRow(t *testing.T) {
	svc := &fakeSessionSvc{one: domain.WASession{ID: "sess_1", Status: domain.SessionWorking}}
	h := newSessionHandlers(svc)
	r := withOrganization(chiReq(http.MethodPost, "/api/v1/sessions/sess_1:start", "", map[string]string{"session": "sess_1"}), testOrganization)
	w := httptest.NewRecorder()
	h.StartSession(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastID != "sess_1" {
		t.Errorf("service saw id %q, want sess_1", svc.lastID)
	}
}

func TestDeleteSession_NoContent(t *testing.T) {
	h := newSessionHandlers(&fakeSessionSvc{})
	r := withOrganization(chiReq(http.MethodDelete, "/api/v1/sessions/sess_1", "", map[string]string{"session": "sess_1"}), testOrganization)
	w := httptest.NewRecorder()
	h.DeleteSession(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}

func TestGetSession_NotFound(t *testing.T) {
	svc := &fakeSessionSvc{err: domain.ErrNotFound("session not found")}
	h := newSessionHandlers(svc)
	r := withOrganization(chiReq(http.MethodGet, "/api/v1/sessions/x", "", map[string]string{"session": "x"}), testOrganization)
	w := httptest.NewRecorder()
	h.GetSession(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestSessionPairingCode_HappyPath(t *testing.T) {
	svc := &fakeSessionSvc{code: "ABCD-1234"}
	h := newSessionHandlers(svc)
	r := withOrganization(chiReq(http.MethodPost, "/api/v1/sessions/sess_1/pairing-code", `{"phone":"62812345"}`, map[string]string{"session": "sess_1"}), testOrganization)
	w := httptest.NewRecorder()
	h.SessionPairingCode(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.lastPhone != "62812345" {
		t.Errorf("phone = %q, want 62812345", svc.lastPhone)
	}
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["code"] != "ABCD-1234" {
		t.Errorf("code = %q, want ABCD-1234", got["code"])
	}
}
