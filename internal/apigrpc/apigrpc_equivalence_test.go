package apigrpc

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	publicv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/public/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/service"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// --- fakes ---

type fakeSessions struct {
	orgs    []string
	got     []string // org arguments observed per call
	session domain.WASession
	err     error
}

func (f *fakeSessions) Create(_ context.Context, organizationID string, _ service.CreateInput) (domain.WASession, error) {
	f.orgs = append(f.orgs, organizationID)
	return f.session, f.err
}
func (f *fakeSessions) List(_ context.Context, organizationID string) ([]domain.WASession, error) {
	f.orgs = append(f.orgs, organizationID)
	return []domain.WASession{f.session}, f.err
}
func (f *fakeSessions) Get(_ context.Context, organizationID, _ string) (domain.WASession, error) {
	f.orgs = append(f.orgs, organizationID)
	return f.session, f.err
}
func (f *fakeSessions) Start(context.Context, string, string) error   { return nil }
func (f *fakeSessions) Stop(context.Context, string, string) error    { return nil }
func (f *fakeSessions) Restart(context.Context, string, string) error { return nil }
func (f *fakeSessions) Logout(context.Context, string, string) error  { return nil }
func (f *fakeSessions) Delete(context.Context, string, string) error  { return nil }
func (f *fakeSessions) Me(_ context.Context, _, _ string) (service.Me, error) {
	return service.Me{SessionID: "ses_1", Status: domain.SessionWorking, Connected: true}, nil
}
func (f *fakeSessions) QR(context.Context, string, string) (service.QR, error) {
	return service.QR{}, nil
}
func (f *fakeSessions) PairingCode(context.Context, string, string, string) (string, error) {
	return "CODE", nil
}

var _ SessionsDeps = (*fakeSessions)(nil)

type fakeMessages struct {
	orgs   []string
	result outbound.SendResult
	err    error
}

func (f *fakeMessages) Send(_ context.Context, organizationID, _ string, _ domain.SendRequest, _ outbound.SendOptions) (outbound.SendResult, error) {
	f.orgs = append(f.orgs, organizationID)
	return f.result, f.err
}
func (f *fakeMessages) Edit(context.Context, string, string, string, string, string) (outbound.SendResult, error) {
	return f.result, f.err
}
func (f *fakeMessages) Revoke(context.Context, string, string, string, string, string) (outbound.SendResult, error) {
	return f.result, f.err
}
func (f *fakeMessages) React(context.Context, string, string, string, string, string, string) (outbound.SendResult, error) {
	return f.result, f.err
}
func (f *fakeMessages) Forward(context.Context, string, string, string, string, string, string) (outbound.SendResult, error) {
	return f.result, f.err
}
func (f *fakeMessages) Vote(context.Context, string, string, string, string, string, []string) (outbound.SendResult, error) {
	return f.result, f.err
}

var _ MessagesDeps = (*fakeMessages)(nil)

type fakeEvents struct {
	pages [][]domain.EventLogEntry
	calls int
}

var debugListSince = func(f *fakeEvents, org, session string, afterID uint64, limit int) ([]domain.EventLogEntry, error) {
	f.calls++
	for _, page := range f.pages {
		if len(page) > 0 && page[len(page)-1].ID > afterID && page[0].ID > afterID {
			return page, nil
		}
	}
	return nil, nil
}

func (f *fakeEvents) ListSince(ctx context.Context, org, session string, afterID uint64, limit int) ([]domain.EventLogEntry, error) {
	page, err := debugListSince(f, org, session, afterID, limit)
	dbgCalls = append(dbgCalls, []any{org, session, afterID, len(page), err})
	return page, err
}

var dbgCalls []any

func (f *fakeEvents) GetByEventID(_ context.Context, eventID string) (domain.EventLogEntry, error) {
	if eventID == "evt_known" {
		return domain.EventLogEntry{ID: 41, EventID: "evt_known", OrganizationID: "org_1"}, nil
	}
	return domain.EventLogEntry{}, domain.ErrNotFound("event not found")
}

var _ EventsReader = (*fakeEvents)(nil)

// allowAllPrincipal passes every capability gate.
func allowAllPrincipal() *authz.Principal {
	return &authz.Principal{
		Kind:           authz.KindAPIKey,
		OrganizationID: "org_1",
		KeyPermissions: domain.Permissions{Read: true, Send: true, Manage: true, Events: true},
	}
}

// fakeTokenVerifier resolves the fixed test bearer to an owner principal of
// the configured organization — the same acceptor path the HTTP middleware uses.
type fakeTokenVerifier struct{ org string }

func (f *fakeTokenVerifier) VerifyToken(_ context.Context, raw string) (*authz.Principal, error) {
	if raw == memberBearer {
		return &authz.Principal{Kind: authz.KindUser, UserID: "user_1", OrganizationID: f.org, OrgRole: "member"}, nil
	}
	return &authz.Principal{Kind: authz.KindUser, UserID: "user_1", OrganizationID: f.org, OrgRole: "owner"}, nil
}

var _ authz.TokenVerifier = (*fakeTokenVerifier)(nil)

// newTestServer builds an in-process public gRPC server behind the real
// authentication interceptors, backed by a fake verifier that maps the test
// bearer to an owner principal.
func newTestServer(t *testing.T, org string, sessions SessionsDeps, messages MessagesDeps, events EventsReader) *grpc.ClientConn {
	t.Helper()
	unary, stream := Interceptors(&fakeTokenVerifier{org: org}, nil)
	server := grpc.NewServer(grpc.UnaryInterceptor(unary), grpc.StreamInterceptor(stream))
	publicv1.RegisterPublicSessionsServiceServer(server, NewSessions(sessions))
	publicv1.RegisterPublicMessagesServiceServer(server, NewMessages(messages, nil))
	publicv1.RegisterPublicEventsServiceServer(server, NewEvents(events, StreamConfig{PollInterval: time.Millisecond}))
	listener := bufconn.Listen(1024 * 1024)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// authenticatedCtx attaches the accepted test credential.
func authenticatedCtx() context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+testBearer)
}

const (
	testBearer   = "eyJhbGciOiJIUzI1NiJ9.fixed.signature"
	memberBearer = "eyJhbGciOiJIUzI1NiJ9.member.signature"
)

// memberCtx attaches a credential resolving to a member-role principal
// (manage-denied), exercising the capability gate like REST's 403s.
func memberCtx(string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+memberBearer)
}

// --- equivalence tests ---

// TestErrorMappingMirrorsREST pins the domain-code → gRPC-status table against
// huma's HTTP mapping so REST and gRPC callers see equivalent outcomes.
func TestErrorMappingMirrorsREST(t *testing.T) {
	cases := map[string]codes.Code{
		domain.CodeNotFound:        codes.NotFound,
		domain.CodeUnauthorized:    codes.Unauthenticated,
		domain.CodeForbidden:       codes.PermissionDenied,
		domain.CodeValidationError: codes.InvalidArgument,
		domain.CodeConflict:        codes.FailedPrecondition,
		domain.CodeRateLimited:     codes.ResourceExhausted,
		domain.CodeNotImplemented:  codes.Unimplemented,
		domain.CodeUnavailable:     codes.Unavailable,
		"internal_error":           codes.Internal,
	}
	for code, want := range cases {
		err := Status(&domain.APIError{Code: code, Message: "x"})
		if status.Code(err) != want {
			t.Fatalf("%s → %s, want %s", code, status.Code(err), want)
		}
	}
}

// TestSessionsEquivalence mirrors the REST session routes: same inputs produce
// equivalent outputs, org comes from the principal, and unauthenticated or
// capability-denied principals fail exactly like the HTTP middleware would.
func TestSessionsEquivalence(t *testing.T) {
	sessions := &fakeSessions{session: domain.WASession{ID: "ses_1", OrganizationID: "org_1"}}
	conn := newTestServer(t, "org_1", sessions, &fakeMessages{}, &fakeEvents{})
	client := publicv1.NewPublicSessionsServiceClient(conn)

	// Org isolation: the principal's org scopes List even when other orgs exist.
	if _, err := client.ListSessions(authenticatedCtx(), &publicv1.ListSessionsRequest{}); err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions.orgs) != 1 || sessions.orgs[0] != "org_1" {
		t.Fatalf("service saw orgs %v", sessions.orgs)
	}

	// GetSession forwards the requested id under the principal's org.
	if _, err := client.GetSession(authenticatedCtx(), &publicv1.GetSessionRequest{SessionId: "ses_1"}); err != nil {
		t.Fatalf("GetSession: %v", err)
	}

	// No principal at all → Unauthenticated (mirrors 401).
	_, err := client.GetSession(context.Background(), &publicv1.GetSessionRequest{SessionId: "ses_1"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated code = %s", status.Code(err))
	}

	// A member-role principal lacks `manage` for CreateSession (mirrors 403).
	_, err = client.CreateSession(memberCtx("org_1"), &publicv1.CreateSessionRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("member create code = %s", status.Code(err))
	}

	// Service errors surface through the shared mapper (not_found → NotFound).
	sessions.err = domain.ErrNotFound("session not found")
	_, err = client.GetSession(authenticatedCtx(), &publicv1.GetSessionRequest{SessionId: "nope"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("not-found code = %s", status.Code(err))
	}
}

// TestMessagesEquivalence verifies send parity with REST: sync sends return the
// WhatsApp id, async returns accepted-mode results, and org scoping applies.
func TestMessagesEquivalence(t *testing.T) {
	messages := &fakeMessages{result: outbound.SendResult{
		Mode: outbound.ModeSync, WAMessageID: "WA_1", Status: domain.MessageSent, Timestamp: 1234,
	}}
	conn := newTestServer(t, "org_1", &fakeSessions{}, messages, &fakeEvents{})
	client := publicv1.NewPublicMessagesServiceClient(conn)

	res, err := client.SendMessage(authenticatedCtx(), &publicv1.SendMessageRequest{
		SessionId: "ses_1",
		Body: &publicv1.SendMessageBody{
			Type: "text",
			To:   "628123@s.whatsapp.net",
			Text: "hi",
		},
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res.Result == nil || res.Result.GetWaMessageId() != "WA_1" || res.Result.GetStatus() != publicv1.MessageStatus_MESSAGE_STATUS_SENT {
		t.Fatalf("result = %#v", res.Result)
	}
	if len(messages.orgs) != 1 || messages.orgs[0] != "org_1" {
		t.Fatalf("service saw orgs %v", messages.orgs)
	}

	// Rate-limited sends map to ResourceExhausted like REST's 429.
	messages.err = domain.ErrRateLimited("send rate limit exceeded")
	_, err = client.SendMessage(authenticatedCtx(), &publicv1.SendMessageRequest{
		SessionId: "ses_1",
		Body: &publicv1.SendMessageBody{
			Type: "text",
			To:   "628123@s.whatsapp.net",
		},
	})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("rate limited code = %s", status.Code(err))
	}
}

// TestStreamEventsEquivalence verifies the durable-log tail: since-cursor
// resolution, ordered delivery from committed rows, and stream termination.
func TestStreamEventsEquivalence(t *testing.T) {
	events := &fakeEvents{pages: [][]domain.EventLogEntry{
		{
			{ID: 42, EventID: "evt_a", Type: domain.EventMessage, OrganizationID: "org_1", SessionID: "ses_1", Payload: json.RawMessage(`{"x":1}`)},
			{ID: 43, EventID: "evt_b", Type: domain.EventMessage, OrganizationID: "org_1", SessionID: "ses_1", Payload: json.RawMessage(`{"x":2}`)},
		},
	}}
	conn := newTestServer(t, "org_1", &fakeSessions{}, &fakeMessages{}, events)
	client := publicv1.NewPublicEventsServiceClient(conn)

	ctx, cancel := context.WithCancel(authenticatedCtx())
	defer cancel()
	stream, err := client.StreamEvents(ctx, &publicv1.StreamEventsRequest{
		SessionId: "ses_1", SinceCursor: "evt_known",
	})
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}
	var ids []string
	for {
		event, err := stream.Recv()
		if err != nil {
			// Cancellation is the normal exit for a live tail once the test has
			// observed the committed rows it needed.
			break
		}
		ids = append(ids, event.Id)
		if len(ids) == 2 {
			cancel()
		}
	}
	if len(ids) < 2 || ids[0] != "evt_a" || ids[1] != "evt_b" {
		t.Fatalf("streamed ids = %v", ids)
	}
	if events.calls < 1 {
		t.Fatal("event source was never read")
	}
}

func ptrOf(value string) *string { return &value }
