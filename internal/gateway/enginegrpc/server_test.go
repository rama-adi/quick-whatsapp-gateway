package enginegrpc

import (
	"context"
	"testing"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeEngine struct {
	presence application.SetPresenceCommand
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
