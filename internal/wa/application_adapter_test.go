package wa

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

var errFakeLiveOp = errors.New("live operation failed")

type fakeSessionStateSource struct {
	status              domain.SessionStatus
	connected, loggedIn bool
	found               bool
}

func (f fakeSessionStateSource) ConnectionState(string) (domain.SessionStatus, bool, bool, bool) {
	return f.status, f.connected, f.loggedIn, f.found
}

type fakeEngineLiveOps struct {
	ctx                               context.Context
	sessionID, state, chatJID, sender string
	messageIDs                        []string
	readAt                            time.Time
	err                               error
	calls                             int
}

func (f *fakeEngineLiveOps) SetPresence(ctx context.Context, sessionID, state string) error {
	f.calls++
	f.ctx, f.sessionID, f.state = ctx, sessionID, state
	return f.err
}

func (f *fakeEngineLiveOps) SendReadReceiptAt(ctx context.Context, sessionID, chatJID, sender string, ids []string, readAt time.Time) error {
	f.calls++
	f.ctx, f.sessionID, f.chatJID, f.sender = ctx, sessionID, chatJID, sender
	f.messageIDs, f.readAt = ids, readAt
	return f.err
}

var adapterNow = time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC)

func testApplicationAdapter(live engineLiveOps) *ApplicationGatewayAdapter {
	return &ApplicationGatewayAdapter{
		gatewayID: "gateway-1",
		sessions: fakeSessionStateSource{
			status: domain.SessionWorking, connected: true, loggedIn: true, found: true,
		},
		live: live, now: func() time.Time { return adapterNow }, maxFutureSkew: DefaultReadReceiptFutureSkew,
	}
}

func assertAPIError(t *testing.T, err error, code, message string) {
	t.Helper()
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *domain.APIError", err)
	}
	if apiErr.Code != code || apiErr.Message != message {
		t.Fatalf("error = (%q, %q), want (%q, %q)", apiErr.Code, apiErr.Message, code, message)
	}
}

func TestApplicationGatewayAdapterSessionState(t *testing.T) {
	a := testApplicationAdapter(&fakeEngineLiveOps{})
	got, err := a.GetSessionState(context.Background(), application.SessionStateQuery{
		OrganizationID: "org-1", SessionID: "session-1", GatewayID: "gateway-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.OrganizationID != "org-1" || got.SessionID != "session-1" || got.GatewayID != "gateway-1" || got.Status != domain.SessionWorking || !got.Connected || !got.LoggedIn {
		t.Fatalf("unexpected state: %#v", got)
	}
}

func TestApplicationGatewayAdapterPreservesContextAndMetadata(t *testing.T) {
	live := &fakeEngineLiveOps{}
	a := testApplicationAdapter(live)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	got, err := a.SetAccountPresence(ctx, application.SetPresenceCommand{
		CommandID: "command-1", OrganizationID: "org-1", SessionID: "session-1",
		GatewayID: "gateway-1", AssignmentEpoch: 7, State: application.AccountPresenceOnline,
	})
	if err != nil {
		t.Fatal(err)
	}
	if live.ctx != ctx || live.sessionID != "session-1" || live.state != "online" || live.calls != 1 {
		t.Fatalf("operation was not delegated faithfully: %#v", live)
	}
	if got.CommandID != "command-1" || got.AssignmentEpoch != 7 || got.OrganizationID != "org-1" || got.SessionID != "session-1" || got.GatewayID != "gateway-1" {
		t.Fatalf("unexpected result metadata: %#v", got)
	}
}

func TestApplicationGatewayAdapterReadReceiptUsesStableTimestamp(t *testing.T) {
	live := &fakeEngineLiveOps{}
	a := testApplicationAdapter(live)
	readAt := adapterNow.Add(-24 * time.Hour) // Old timestamps are valid; only future skew is bounded.
	ctx := context.Background()

	_, err := a.MarkRead(ctx, application.MarkReadCommand{
		CommandID: "command-2", OrganizationID: "org-1", SessionID: "session-1",
		GatewayID: "gateway-1", AssignmentEpoch: 8, ChatJID: "123@g.us",
		SenderJID: "456@s.whatsapp.net", MessageIDs: []string{"message-1"}, ReadAt: readAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if live.ctx != ctx || live.chatJID != "123@g.us" || live.sender != "456@s.whatsapp.net" || len(live.messageIDs) != 1 || live.messageIDs[0] != "message-1" || !live.readAt.Equal(readAt) || live.calls != 1 {
		t.Fatalf("read receipt was not delegated faithfully: %#v", live)
	}
}

func TestApplicationGatewayAdapterValidationDoesNotDelegate(t *testing.T) {
	validPresence := application.SetPresenceCommand{CommandID: "c", OrganizationID: "o", SessionID: "s", GatewayID: "gateway-1", AssignmentEpoch: 1, State: application.AccountPresenceOnline}
	validRead := application.MarkReadCommand{CommandID: "c", OrganizationID: "o", SessionID: "s", GatewayID: "gateway-1", AssignmentEpoch: 1, ChatJID: "1@g.us", SenderJID: "2@s.whatsapp.net", MessageIDs: []string{"m"}, ReadAt: adapterNow}
	tests := []struct {
		name, message string
		call          func(*ApplicationGatewayAdapter) error
	}{
		{"missing command", "command_id is required", func(a *ApplicationGatewayAdapter) error {
			c := validPresence
			c.CommandID = ""
			_, err := a.SetAccountPresence(context.Background(), c)
			return err
		}},
		{"missing organization", "organization_id, session_id, and gateway_id are required", func(a *ApplicationGatewayAdapter) error {
			c := validPresence
			c.OrganizationID = ""
			_, err := a.SetAccountPresence(context.Background(), c)
			return err
		}},
		{"missing session", "organization_id, session_id, and gateway_id are required", func(a *ApplicationGatewayAdapter) error {
			c := validPresence
			c.SessionID = ""
			_, err := a.SetAccountPresence(context.Background(), c)
			return err
		}},
		{"missing gateway", "organization_id, session_id, and gateway_id are required", func(a *ApplicationGatewayAdapter) error {
			c := validPresence
			c.GatewayID = ""
			_, err := a.SetAccountPresence(context.Background(), c)
			return err
		}},
		{"zero presence epoch", "assignment_epoch must be at least 1", func(a *ApplicationGatewayAdapter) error {
			c := validPresence
			c.AssignmentEpoch = 0
			_, err := a.SetAccountPresence(context.Background(), c)
			return err
		}},
		{"unknown presence", "presence state must be online or offline", func(a *ApplicationGatewayAdapter) error {
			c := validPresence
			c.State = "away"
			_, err := a.SetAccountPresence(context.Background(), c)
			return err
		}},
		{"zero read epoch", "assignment_epoch must be at least 1", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.AssignmentEpoch = 0
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"empty IDs", "chat_jid, message_ids, and read_at are required", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.MessageIDs = nil
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"all-empty IDs", "message_ids must not contain empty values", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.MessageIDs = []string{"", ""}
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"zero timestamp", "chat_jid, message_ids, and read_at are required", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.ReadAt = time.Time{}
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"future timestamp", "read_at exceeds allowed future clock skew", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.ReadAt = adapterNow.Add(DefaultReadReceiptFutureSkew + time.Nanosecond)
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"invalid chat JID", "chat_jid is invalid", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.ChatJID = "a@b@c"
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"invalid sender JID", "sender_jid is invalid", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.SenderJID = "a@b@c"
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
		{"group missing sender JID", "sender_jid is required for group read receipts", func(a *ApplicationGatewayAdapter) error {
			c := validRead
			c.SenderJID = ""
			_, err := a.MarkRead(context.Background(), c)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live := &fakeEngineLiveOps{}
			err := tt.call(testApplicationAdapter(live))
			assertAPIError(t, err, domain.CodeValidationError, tt.message)
			if live.calls != 0 {
				t.Fatalf("validation failure delegated %d live calls", live.calls)
			}
		})
	}
}

func TestApplicationGatewayAdapterExactTargetAndLiveErrors(t *testing.T) {
	live := &fakeEngineLiveOps{err: errFakeLiveOp}
	a := testApplicationAdapter(live)
	_, err := a.GetSessionState(context.Background(), application.SessionStateQuery{OrganizationID: "o", SessionID: "s", GatewayID: "other"})
	assertAPIError(t, err, domain.CodeNotFound, "gateway target does not match this gateway")

	_, err = a.SetAccountPresence(context.Background(), application.SetPresenceCommand{CommandID: "c", OrganizationID: "o", SessionID: "s", GatewayID: "gateway-1", AssignmentEpoch: 1, State: application.AccountPresenceOnline})
	if !errors.Is(err, errFakeLiveOp) {
		t.Fatalf("error = %v, want live error", err)
	}
	if live.calls != 1 {
		t.Fatalf("live calls = %d, want 1", live.calls)
	}

	live.err = errFakeLiveOp
	_, err = a.MarkRead(context.Background(), application.MarkReadCommand{
		CommandID: "c2", OrganizationID: "o", SessionID: "s", GatewayID: "gateway-1",
		AssignmentEpoch: 1, ChatJID: "1@g.us", SenderJID: "2@s.whatsapp.net", MessageIDs: []string{"m"}, ReadAt: adapterNow,
	})
	if !errors.Is(err, errFakeLiveOp) {
		t.Fatalf("read error = %v, want live error", err)
	}
	if live.calls != 2 {
		t.Fatalf("live calls = %d, want 2", live.calls)
	}
}

func TestApplicationGatewayAdapterSessionNotFoundError(t *testing.T) {
	a := testApplicationAdapter(&fakeEngineLiveOps{})
	a.sessions = fakeSessionStateSource{}
	_, err := a.GetSessionState(context.Background(), application.SessionStateQuery{OrganizationID: "o", SessionID: "missing", GatewayID: "gateway-1"})
	assertAPIError(t, err, domain.CodeNotFound, "session not found")
}
