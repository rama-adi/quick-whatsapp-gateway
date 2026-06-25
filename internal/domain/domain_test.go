package domain

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewULID_FormatAndUniqueness(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	var prev string
	for i := 0; i < n; i++ {
		id := NewULID()
		// A canonical ULID is 26 chars of Crockford base32.
		if len(id) != 26 {
			t.Fatalf("ULID length = %d, want 26 (%q)", len(id), id)
		}
		if strings.ToUpper(id) != id {
			t.Errorf("ULID should be upper-case Crockford base32, got %q", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate ULID generated: %q", id)
		}
		seen[id] = struct{}{}
		// Monotonic within (and across) a millisecond: each is >= the previous.
		if prev != "" && id < prev {
			t.Errorf("ULID not monotonic: %q < %q", id, prev)
		}
		prev = id
	}
}

func TestNewULID_ConcurrentSafe(t *testing.T) {
	const goroutines = 50
	const per = 200
	var mu sync.Mutex
	seen := make(map[string]struct{}, goroutines*per)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]string, 0, per)
			for i := 0; i < per; i++ {
				local = append(local, NewULID())
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range local {
				if _, dup := seen[id]; dup {
					t.Errorf("duplicate ULID under concurrency: %q", id)
				}
				seen[id] = struct{}{}
			}
		}()
	}
	wg.Wait()
	if len(seen) != goroutines*per {
		t.Errorf("got %d unique ULIDs, want %d", len(seen), goroutines*per)
	}
}

func TestNewPrefixedIDs(t *testing.T) {
	cases := []struct {
		fn     func() string
		prefix string
	}{
		{NewEventID, PrefixEvent},
		{NewSessionID, PrefixSession},
		{NewTenantID, PrefixTenant},
		{NewAPIKeyID, PrefixAPIKey},
		{NewWebhookID, PrefixWebhook},
		{NewOutboxID, PrefixOutbox},
	}
	for _, c := range cases {
		id := c.fn()
		if !strings.HasPrefix(id, c.prefix) {
			t.Errorf("id %q missing prefix %q", id, c.prefix)
		}
		if got := len(id) - len(c.prefix); got != 26 {
			t.Errorf("id %q body length = %d, want 26", id, got)
		}
	}
}

func TestNowMs(t *testing.T) {
	before := time.Now().UnixMilli()
	got := NowMs()
	after := time.Now().UnixMilli()
	if got < before || got > after {
		t.Errorf("NowMs() = %d, want within [%d, %d]", got, before, after)
	}
}

func TestSessionStatusFromName(t *testing.T) {
	tests := []struct {
		name   string
		want   SessionStatus
		wantOK bool
	}{
		{"STARTING", SessionStarting, true},
		{"SCAN_QR_CODE", SessionScanQR, true},
		{"WORKING", SessionWorking, true},
		{"FAILED", SessionFailed, true},
		{"STOPPED", SessionStopped, true},
		{"LOGGED_OUT", SessionLoggedOut, true},
		{"unknown", "", false},
		{"working", "", false}, // case-sensitive: lowercase is not a §3 name
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := SessionStatusFromName(tt.name)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("SessionStatusFromName(%q) = (%q, %v), want (%q, %v)",
				tt.name, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestNewEvent_ShapeAndJSONTags(t *testing.T) {
	payload := map[string]any{"foo": "bar"}
	before := NowMs()
	ev := NewEvent(EventMessage, "sess_123", "ten_abc", payload)
	after := NowMs()

	if ev.Schema != Schema {
		t.Errorf("Schema = %q, want %q", ev.Schema, Schema)
	}
	if !strings.HasPrefix(ev.ID, PrefixEvent) {
		t.Errorf("ID = %q, want %s prefix", ev.ID, PrefixEvent)
	}
	if ev.Type != EventMessage {
		t.Errorf("Type = %q, want %q", ev.Type, EventMessage)
	}
	if ev.Session != "sess_123" || ev.Tenant != "ten_abc" {
		t.Errorf("Session/Tenant = %q/%q", ev.Session, ev.Tenant)
	}
	if ev.Timestamp < before || ev.Timestamp > after {
		t.Errorf("Timestamp = %d, want within [%d, %d]", ev.Timestamp, before, after)
	}

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	// The wire keys per §9 — note "event" maps to the Go Type field.
	for _, key := range []string{"schema", "id", "event", "session", "tenant", "timestamp", "payload"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("event JSON missing key %q (have %v)", key, raw)
		}
	}
	if _, ok := raw["type"]; ok {
		t.Errorf("event JSON should use \"event\" not \"type\"")
	}
}

func TestAPIError_Constructors(t *testing.T) {
	tests := []struct {
		ctor func(string) *APIError
		code string
	}{
		{ErrRateLimited, CodeRateLimited},
		{ErrNotFound, CodeNotFound},
		{ErrUnauthorized, CodeUnauthorized},
		{ErrForbidden, CodeForbidden},
		{ErrValidation, CodeValidationError},
		{ErrConflict, CodeConflict},
		{ErrNotImplemented, CodeNotImplemented},
		{ErrInternal, CodeInternal},
	}
	for _, tt := range tests {
		e := tt.ctor("boom")
		if e.Code != tt.code {
			t.Errorf("code = %q, want %q", e.Code, tt.code)
		}
		if e.Message != "boom" {
			t.Errorf("message = %q, want %q", e.Message, "boom")
		}
		if e.Details != nil {
			t.Errorf("details should default nil, got %v", e.Details)
		}
		// Error() implements the error interface as "code: message".
		if want := tt.code + ": boom"; e.Error() != want {
			t.Errorf("Error() = %q, want %q", e.Error(), want)
		}
		// As a Go error value.
		var asErr error = e
		if asErr.Error() != tt.code+": boom" {
			t.Errorf("error interface mismatch: %q", asErr.Error())
		}
	}
}

func TestAPIError_WithDetailsAndEnvelopeJSON(t *testing.T) {
	e := NewAPIError(CodeValidationError, "bad field").
		WithDetails(map[string]any{"field": "to"})
	if e.Details["field"] != "to" {
		t.Errorf("WithDetails not attached: %v", e.Details)
	}

	body := ErrorBody{Error: e}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal error body: %v", err)
	}
	got := string(b)
	for _, sub := range []string{`"error"`, `"code":"validation_error"`, `"message":"bad field"`, `"details"`, `"field":"to"`} {
		if !strings.Contains(got, sub) {
			t.Errorf("error body JSON %q missing %q", got, sub)
		}
	}

	// Without details, the omitempty tag drops the key.
	plain, _ := json.Marshal(ErrorBody{Error: NewAPIError(CodeNotFound, "nope")})
	if strings.Contains(string(plain), "details") {
		t.Errorf("details should be omitted when nil: %s", plain)
	}
}
