package service

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// fakeResolver is a clientResolver test double for the routing WAClient.
type fakeResolver struct {
	cli    *whatsmeow.Client
	ok     bool
	lastID string
}

func (f *fakeResolver) ClientFor(sessionID string) (*whatsmeow.Client, bool) {
	f.lastID = sessionID
	return f.cli, f.ok
}

// TestRoutingWAClient_NoSessionOnContext_NotImplemented calls the account-global routing adapter without
// stamping a session id on context. It must return not_implemented before resolving or dereferencing any
// whatsmeow client. A send can never fall through to an arbitrary live session.
func TestRoutingWAClient_NoSessionOnContext_NotImplemented(t *testing.T) {
	c := NewRoutingWAClient(&fakeResolver{ok: false})
	// No outbound.WithSessionID on the context.
	_, _, err := c.SendText(context.Background(), "a@s.whatsapp.net", "hi", outbound.QuoteInfo{}, nil)
	assertNotImplemented(t, err)
}

// TestRoutingWAClient_UnresolvableSession_NotImplemented stamps a session id that the manager cannot
// resolve to a live client. Every routed WhatsApp operation must fail with the same not_implemented domain
// error rather than panic. This makes stopped and test-only sessions explicit at the service boundary.
func TestRoutingWAClient_UnresolvableSession_NotImplemented(t *testing.T) {
	res := &fakeResolver{ok: false}
	c := NewRoutingWAClient(res)
	ctx := outbound.WithSessionID(context.Background(), "sess_1")

	// Every method should surface not_implemented when the session has no live
	// client, and should have asked the resolver for the right session.
	checks := []func() error{
		func() error {
			_, _, e := c.SendText(ctx, "a@s.whatsapp.net", "hi", outbound.QuoteInfo{}, nil)
			return e
		},
		func() error {
			_, _, e := c.SendPoll(ctx, "a@s.whatsapp.net", "q", []string{"x"}, 1, 0, false)
			return e
		},
		func() error { _, _, e := c.SendLocation(ctx, "a@s.whatsapp.net", 1, 2, ""); return e },
		func() error { _, _, e := c.SendContact(ctx, "a@s.whatsapp.net", "n", "p", ""); return e },
		func() error { _, _, e := c.React(ctx, "a@s.whatsapp.net", "", "m", "👍"); return e },
		func() error { _, _, e := c.Edit(ctx, "a@s.whatsapp.net", "m", "new"); return e },
		func() error { _, _, e := c.Revoke(ctx, "a@s.whatsapp.net", "", "m"); return e },
		func() error { _, _, e := c.Vote(ctx, "a@s.whatsapp.net", "", "m", []string{"x"}); return e },
		func() error { _, _, e := c.Forward(ctx, "b@s.whatsapp.net", "a@s.whatsapp.net", "", "m"); return e },
	}
	for i, fn := range checks {
		if err := fn(); !isNotImplemented(err) {
			t.Errorf("check %d: err = %v, want not_implemented", i, err)
		}
	}
	if res.lastID != "sess_1" {
		t.Errorf("resolver session id = %q, want sess_1", res.lastID)
	}
}

// TestSessionIDContextRoundTrip writes a session id with outbound.WithSessionID and reads it through the
// routing helper, including the empty-context case. The value must survive unchanged and absence must
// remain empty. This context key is the only per-request selector used by the account-global Sender.
func TestSessionIDContextRoundTrip(t *testing.T) {
	if got := outbound.SessionIDFromContext(context.Background()); got != "" {
		t.Errorf("empty ctx session = %q, want \"\"", got)
	}
	ctx := outbound.WithSessionID(context.Background(), "sess_9")
	if got := outbound.SessionIDFromContext(ctx); got != "sess_9" {
		t.Errorf("session = %q, want sess_9", got)
	}
}

func assertNotImplemented(t *testing.T, err error) {
	t.Helper()
	if !isNotImplemented(err) {
		t.Fatalf("err = %v, want not_implemented", err)
	}
}

func isNotImplemented(err error) bool {
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == domain.CodeNotImplemented
}
