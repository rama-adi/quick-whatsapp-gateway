package main

import (
	"encoding/json"
	"testing"

	publicv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/public/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// runE2EPublicGRPCScenarios reaches the API's production public gRPC listener
// with the same seeded organizations and live gateway as the REST scenarios.
func runE2EPublicGRPCScenarios(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	conn, err := grpc.NewClient(infra.apiGRPC, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	health := publicv1.NewPublicHealthServiceClient(conn)
	sessions := publicv1.NewPublicSessionsServiceClient(conn)
	messages := publicv1.NewPublicMessagesServiceClient(conn)
	events := publicv1.NewPublicEventsServiceClient(conn)

	t.Run("public gRPC health and organization authorization", func(t *testing.T) {
		ctx, cancel := e2eContext(t)
		defer cancel()
		check, err := health.Check(ctx, &publicv1.PublicHealthServiceCheckRequest{})
		if err != nil || check.GetStatus() != publicv1.ServingStatus_SERVING_STATUS_SERVING {
			t.Fatalf("gRPC health = %v, %v", check, err)
		}
		if _, err := sessions.GetSession(ctx, &publicv1.GetSessionRequest{SessionId: e2eSessionID}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("uncredentialed gRPC session lookup = %v", err)
		}
		orgA := metadata.AppendToOutgoingContext(ctx, "x-api-key", e2eOrgAKey)
		orgB := metadata.AppendToOutgoingContext(ctx, "x-api-key", e2eOrgBKey)
		own, err := sessions.GetSession(orgA, &publicv1.GetSessionRequest{SessionId: e2eSessionID})
		if err != nil || own.GetSession().GetId() != e2eSessionID {
			t.Fatalf("own gRPC session lookup = %v, %v", own, err)
		}
		me, err := sessions.GetMe(orgA, &publicv1.GetMeRequest{SessionId: e2eSessionID})
		if err != nil || me.GetMe().GetSessionId() != e2eSessionID || me.GetMe().GetWaJid() != e2eDeviceJID {
			t.Fatalf("paired gRPC self identity = %v, %v", me, err)
		}
		if _, err := sessions.GetSession(orgB, &publicv1.GetSessionRequest{SessionId: e2eSessionID}); status.Code(err) != codes.NotFound {
			t.Fatalf("cross-org gRPC session lookup = %v", err)
		}
		listed, err := sessions.ListSessions(orgB, &publicv1.ListSessionsRequest{})
		if err != nil || len(listed.GetSessions()) != 0 {
			t.Fatalf("cross-org gRPC session list = %v, %v", listed, err)
		}
	})

	t.Run("public gRPC session lifecycle and pairing", func(t *testing.T) {
		ctx, cancel := e2eContext(t)
		defer cancel()
		orgA := metadata.AppendToOutgoingContext(ctx, "x-api-key", e2eOrgAKey)
		orgB := metadata.AppendToOutgoingContext(ctx, "x-api-key", e2eOrgBKey)
		created, err := sessions.CreateSession(orgA, &publicv1.CreateSessionRequest{Label: grpcString("gRPC E2E pairing")})
		if err != nil || created.GetSession().GetId() == "" {
			t.Fatalf("create gRPC session = %v, %v", created, err)
		}
		id := created.GetSession().GetId()
		listed, err := sessions.ListSessions(orgA, &publicv1.ListSessionsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, session := range listed.GetSessions() {
			found = found || session.GetId() == id
		}
		if !found {
			t.Fatalf("gRPC-created session absent from list: %q", id)
		}
		if _, err := sessions.GetSession(orgB, &publicv1.GetSessionRequest{SessionId: id}); status.Code(err) != codes.NotFound {
			t.Fatalf("cross-org gRPC get session = %v", err)
		}
		if _, err := sessions.GetMe(orgA, &publicv1.GetMeRequest{SessionId: id}); status.Code(err) != codes.NotFound {
			t.Fatalf("unpaired gRPC GetMe = %v", err)
		}
		if _, err := sessions.CreatePairingCode(orgA, &publicv1.CreatePairingCodeRequest{SessionId: id}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("empty gRPC pairing phone = %v", err)
		}
		pairing, err := sessions.CreatePairingCode(orgA, &publicv1.CreatePairingCodeRequest{SessionId: id, Phone: "628777000333"})
		if err != nil || pairing.GetCode() != "E2E-CODE" {
			t.Fatalf("gRPC pairing = %v, %v", pairing, err)
		}
		e2eEventually(t, ctx, "gRPC QR code", func() bool {
			qr, err := sessions.GetSessionQrCode(orgA, &publicv1.GetSessionQrCodeRequest{SessionId: id})
			return err == nil && qr.GetQrCode().GetCode() == "isolated-e2e-qr"
		})
		if started, err := sessions.StartSession(orgA, &publicv1.StartSessionRequest{SessionId: id}); err != nil || started.GetSession().GetId() != id {
			t.Fatalf("gRPC start = %v, %v", started, err)
		}
		if stopped, err := sessions.StopSession(orgA, &publicv1.StopSessionRequest{SessionId: id}); err != nil || stopped.GetSession().GetId() != id {
			t.Fatalf("gRPC stop = %v, %v", stopped, err)
		}
		if restarted, err := sessions.RestartSession(orgA, &publicv1.RestartSessionRequest{SessionId: id}); err != nil || restarted.GetSession().GetId() != id {
			t.Fatalf("gRPC restart = %v, %v", restarted, err)
		}
		if loggedOut, err := sessions.LogoutSession(orgA, &publicv1.LogoutSessionRequest{SessionId: id}); err != nil || loggedOut.GetSession().GetId() != id {
			t.Fatalf("gRPC logout = %v, %v", loggedOut, err)
		}
		if _, err := sessions.DeleteSession(orgA, &publicv1.DeleteSessionRequest{SessionId: id}); err != nil {
			t.Fatal(err)
		}
		if _, err := sessions.GetSession(orgA, &publicv1.GetSessionRequest{SessionId: id}); status.Code(err) != codes.NotFound {
			t.Fatalf("deleted gRPC session = %v", err)
		}
	})

	t.Run("public gRPC sent message history and committed event", func(t *testing.T) {
		ctx, cancel := e2eContext(t)
		defer cancel()
		orgA := metadata.AppendToOutgoingContext(ctx, "x-api-key", e2eOrgAKey)
		orgB := metadata.AppendToOutgoingContext(ctx, "x-api-key", e2eOrgBKey)
		stream, err := events.StreamEvents(orgA, &publicv1.StreamEventsRequest{
			SessionId: e2eSessionID, EventTypes: []string{domain.EventMessageFromMe},
		})
		if err != nil {
			t.Fatal(err)
		}
		before := len(gateway.getCaptures(t))
		sent, err := messages.SendMessage(orgA, &publicv1.SendMessageRequest{
			SessionId: e2eSessionID, IdempotencyKey: "grpc-sent-history-1",
			Body: &publicv1.SendMessageBody{Type: "text", To: e2eGroupJID, Text: "gRPC sent message"},
		})
		if err != nil || sent.GetResult().GetWaMessageId() == "" {
			t.Fatalf("gRPC send = %v, %v; API logs=%s", sent, err, infra.apiOutput.String())
		}
		gateway.waitCaptureCount(t, before+1)
		page, err := messages.ListMessages(orgA, &publicv1.ListMessagesRequest{
			SessionId: e2eSessionID, ChatJid: e2eGroupJID,
		})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, message := range page.GetMessages() {
			if message.GetWaMessageId() == sent.GetResult().GetWaMessageId() {
				found = message.GetFromMe()
			}
		}
		if !found {
			t.Fatalf("gRPC send absent from committed history: id=%q", sent.GetResult().GetWaMessageId())
		}
		if _, err := messages.SendMessage(orgB, &publicv1.SendMessageRequest{
			SessionId: e2eSessionID, Body: &publicv1.SendMessageBody{Type: "text", To: e2eGroupJID, Text: "cross-org"},
		}); status.Code(err) != codes.NotFound {
			t.Fatalf("cross-org gRPC send = %v", err)
		}
		if _, err := messages.ListMessages(orgB, &publicv1.ListMessagesRequest{
			SessionId: e2eSessionID, ChatJid: e2eGroupJID,
		}); status.Code(err) != codes.NotFound {
			t.Fatalf("cross-org gRPC history = %v", err)
		}
		for {
			event, err := stream.Recv()
			if err != nil {
				t.Fatalf("gRPC committed event stream: %v", err)
			}
			if event.GetOrganizationId() != e2eOrgA || event.GetSessionId() != e2eSessionID {
				t.Fatalf("gRPC event crossed organization/session: %v", event)
			}
			var payload struct {
				WAMessageID string `json:"waMessageId"`
			}
			if err := json.Unmarshal(event.GetPayloadJson(), &payload); err != nil {
				t.Fatal(err)
			}
			if event.GetEvent() == domain.EventMessageFromMe && payload.WAMessageID == sent.GetResult().GetWaMessageId() {
				break
			}
		}
	})

	t.Run("public gRPC message operation payloads", func(t *testing.T) {
		ctx, cancel := e2eContext(t)
		defer cancel()
		orgA := metadata.AppendToOutgoingContext(ctx, "x-api-key", e2eOrgAKey)
		source, err := messages.SendMessage(orgA, &publicv1.SendMessageRequest{
			SessionId: e2eSessionID, IdempotencyKey: "grpc-ops-source",
			Body: &publicv1.SendMessageBody{Type: "text", To: e2eGroupJID, Text: "gRPC original"},
		})
		if err != nil || source.GetResult().GetWaMessageId() == "" {
			t.Fatalf("gRPC source message = %v, %v", source, err)
		}
		messageID := source.GetResult().GetWaMessageId()
		checkCapture := func(name, expectedID string, check func(*waE2E.Message) bool) {
			t.Helper()
			captures := gateway.getCaptures(t)
			for _, capture := range captures {
				if capture.ID != expectedID {
					continue
				}
				var message waE2E.Message
				if err := protojson.Unmarshal(capture.Message, &message); err != nil {
					t.Fatal(err)
				}
				if !check(&message) {
					t.Fatalf("%s emitted wrong WhatsApp payload", name)
				}
				return
			}
			t.Fatalf("%s missing gateway capture %q", name, expectedID)
		}
		edited, err := messages.EditMessage(orgA, &publicv1.EditMessageRequest{
			SessionId: e2eSessionID, MessageId: messageID, Chat: e2eGroupJID, Text: "gRPC edited",
		})
		if err != nil || edited.GetResult().GetWaMessageId() == "" {
			t.Fatalf("gRPC edit = %v, %v", edited, err)
		}
		checkCapture("edit", edited.GetResult().GetWaMessageId(), func(m *waE2E.Message) bool {
			return m.GetEditedMessage().GetMessage().GetProtocolMessage().GetEditedMessage().GetConversation() == "gRPC edited"
		})
		added, err := messages.AddReaction(orgA, &publicv1.AddReactionRequest{
			SessionId: e2eSessionID, MessageId: messageID, Chat: e2eGroupJID, Emoji: "👍",
		})
		if err != nil || added.GetResult().GetWaMessageId() == "" {
			t.Fatalf("gRPC add reaction = %v, %v", added, err)
		}
		checkCapture("add reaction", added.GetResult().GetWaMessageId(), func(m *waE2E.Message) bool {
			return m.GetReactionMessage().GetText() == "👍"
		})
		removed, err := messages.RemoveReaction(orgA, &publicv1.RemoveReactionRequest{
			SessionId: e2eSessionID, MessageId: messageID, Chat: e2eGroupJID,
		})
		if err != nil || removed.GetResult().GetWaMessageId() == "" {
			t.Fatalf("gRPC remove reaction = %v, %v", removed, err)
		}
		checkCapture("remove reaction", removed.GetResult().GetWaMessageId(), func(m *waE2E.Message) bool {
			return m.GetReactionMessage() != nil && m.GetReactionMessage().GetText() == ""
		})
		forwarded, err := messages.ForwardMessage(orgA, &publicv1.ForwardMessageRequest{
			SessionId: e2eSessionID, MessageId: messageID, Chat: e2eGroupJID,
			To: "120363000000000002@g.us",
		})
		if err != nil || forwarded.GetResult().GetWaMessageId() == "" {
			t.Fatalf("gRPC forward = %v, %v", forwarded, err)
		}
		checkCapture("forward", forwarded.GetResult().GetWaMessageId(), func(m *waE2E.Message) bool {
			return m.GetExtendedTextMessage().GetContextInfo().GetIsForwarded()
		})
		revoked, err := messages.RevokeMessage(orgA, &publicv1.RevokeMessageRequest{
			SessionId: e2eSessionID, MessageId: messageID, Chat: e2eGroupJID,
		})
		if err != nil || revoked.GetResult().GetWaMessageId() == "" {
			t.Fatalf("gRPC revoke = %v, %v", revoked, err)
		}
		checkCapture("revoke", revoked.GetResult().GetWaMessageId(), func(m *waE2E.Message) bool {
			return m.GetProtocolMessage() != nil
		})
		poll, err := messages.SendMessage(orgA, &publicv1.SendMessageRequest{
			SessionId: e2eSessionID, IdempotencyKey: "grpc-ops-poll",
			Body: &publicv1.SendMessageBody{Type: "poll", To: e2eGroupJID, Name: "gRPC vote?", Options: []string{"A", "B"}, SelectableCount: 1},
		})
		if err != nil || poll.GetResult().GetWaMessageId() == "" {
			t.Fatalf("gRPC poll source = %v, %v", poll, err)
		}
		voted, err := messages.VotePoll(orgA, &publicv1.VotePollRequest{
			SessionId: e2eSessionID, MessageId: poll.GetResult().GetWaMessageId(),
			Chat: e2eGroupJID, Sender: e2eDeviceLID, Options: []string{"A"},
		})
		if err != nil || voted.GetResult().GetWaMessageId() == "" {
			t.Fatalf("gRPC vote = %v, %v", voted, err)
		}
		checkCapture("vote", voted.GetResult().GetWaMessageId(), func(m *waE2E.Message) bool {
			key := m.GetPollUpdateMessage().GetPollCreationMessageKey()
			return key.GetID() == poll.GetResult().GetWaMessageId() && key.GetParticipant() == e2eDeviceLID && key.GetFromMe()
		})
	})
}

func grpcString(value string) *string { return &value }
