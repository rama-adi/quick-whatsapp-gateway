package enginegrpc

import (
	"context"
	"testing"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeEngine struct {
	presence application.SetPresenceCommand
	sent     []application.SendCommand
	ops      []application.MessageOpCommand
}

func (f *fakeEngine) GetSessionState(context.Context, application.SessionStateQuery) (application.SessionState, error) {
	return application.SessionState{Status: "working", Connected: true, LoggedIn: true}, nil
}
func (f *fakeEngine) SetAccountPresence(_ context.Context, value application.SetPresenceCommand) (application.MutationResult, error) {
	f.presence = value
	return application.MutationResult{CommandID: value.CommandID, OrganizationID: value.OrganizationID, SessionID: value.SessionID, GatewayID: value.GatewayID, AssignmentEpoch: value.AssignmentEpoch}, nil
}
func (f *fakeEngine) MarkRead(context.Context, application.MarkReadCommand) (application.MutationResult, error) {
	return application.MutationResult{}, nil
}

func (f *fakeEngine) ExecuteOp(_ context.Context, command application.MessageOpCommand) (application.MessageOpResult, error) {
	f.ops = append(f.ops, command)
	return application.MessageOpResult{MutationResult: application.MutationResult{CommandID: command.CommandID, OrganizationID: command.OrganizationID, SessionID: command.SessionID, GatewayID: command.GatewayID, AssignmentEpoch: command.AssignmentEpoch}, WAMessageID: "WA_OP_1"}, nil
}

func (f *fakeEngine) SendMessage(_ context.Context, command application.SendCommand) (application.SendMessageResult, error) {
	f.sent = append(f.sent, command)
	return application.SendMessageResult{
		MutationResult: application.MutationResult{CommandID: command.CommandID, OrganizationID: command.OrganizationID, SessionID: command.SessionID, GatewayID: command.GatewayID, AssignmentEpoch: command.AssignmentEpoch},
		WAMessageID:    "WA_TEST_1",
		SentAt:         time.UnixMilli(1234).UTC(),
	}, nil
}

func TestSetPresenceRejectsForeignGatewayAndMapsFenceError(t *testing.T) {
	engine := &fakeEngine{}
	server := &Server{GatewayID: "gw", Engine: engine}
	_, err := server.SetAccountPresence(context.Background(), &gatewayv1.SetAccountPresenceRequest{Target: &gatewayv1.SessionTarget{OrganizationId: "org", SessionId: "s", GatewayId: "other"}, AssignmentEpoch: 1, CommandId: "c", State: gatewayv1.AccountPresence_ACCOUNT_PRESENCE_ONLINE})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("foreign gateway code = %s", status.Code(err))
	}
	_, err = server.SetAccountPresence(context.Background(), &gatewayv1.SetAccountPresenceRequest{Target: &gatewayv1.SessionTarget{OrganizationId: "org", SessionId: "s", GatewayId: "gw"}, AssignmentEpoch: 1, CommandId: "c", State: gatewayv1.AccountPresence_ACCOUNT_PRESENCE_ONLINE})
	if err != nil || engine.presence.GatewayID != "gw" {
		t.Fatalf("presence = %#v, %v", engine.presence, err)
	}
}

func TestSendMessageCarriesPayloadAndRejectsBadJSON(t *testing.T) {
	engine := &fakeEngine{}
	server := &Server{GatewayID: "gw", Engine: engine}
	response, err := server.SendMessage(context.Background(), &gatewayv1.SendMessageRequest{
		Target:          &gatewayv1.SessionTarget{OrganizationId: "org", SessionId: "s", GatewayId: "gw"},
		AssignmentEpoch: 3, CommandId: "cmd_1", PayloadJson: []byte(`{"type":"text","to":"628123@s.whatsapp.net","text":"hi"}`),
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if response.CommandId != "cmd_1" || response.WaMessageId != "WA_TEST_1" || response.SentAtUnixMs != 1234 {
		t.Fatalf("response = %#v", response)
	}
	if len(engine.sent) != 1 || engine.sent[0].Payload.Text != "hi" || engine.sent[0].Payload.Type != "text" {
		t.Fatalf("payload not carried: %#v", engine.sent)
	}

	_, err = server.SendMessage(context.Background(), &gatewayv1.SendMessageRequest{
		Target:          &gatewayv1.SessionTarget{OrganizationId: "org", SessionId: "s", GatewayId: "gw"},
		AssignmentEpoch: 3, CommandId: "cmd_2", PayloadJson: []byte(`{not-json`),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad json code = %s", status.Code(err))
	}
}
