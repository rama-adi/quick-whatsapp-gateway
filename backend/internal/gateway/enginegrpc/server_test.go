package enginegrpc

import (
	"context"
	"testing"
	"time"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeEngine struct {
	presence application.SetPresenceCommand
	sent     []application.SendCommand
	ops      []application.MessageOpCommand

	lookups        []application.LookupContactCommand
	pictures       []string
	abouts         []string
	blocks         []application.ContactJIDCommand
	groupCommands  []application.GroupMutationCommand
	inviteResets   []bool
	joins          []string
	chatPresence   []application.ChatPresenceCommand
	subscribed     []string
	backfillCalled bool

	prepared    []application.SessionStateQuery
	beginPair   application.SessionStateQuery
	pairSnap    application.PairingSnapshot
	pairPhones  []string
	pairingCode string
	logouts     []application.ContactJIDCommand
	forgotten   []string
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

func (f *fakeEngine) LookupContact(_ context.Context, command application.LookupContactCommand) ([]application.ContactLookup, error) {
	f.lookups = append(f.lookups, command)
	return []application.ContactLookup{{Query: command.Phones[0], JID: "628123@s.whatsapp.net", IsIn: true}}, nil
}

func (f *fakeEngine) GetContactPicture(_ context.Context, _ application.SessionStateQuery, jid string) (domain.ProfilePicture, error) {
	f.pictures = append(f.pictures, jid)
	return domain.ProfilePicture{URL: "https://pfp.example/1.jpg", ID: "pic_1"}, nil
}

func (f *fakeEngine) GetContactAbout(_ context.Context, _ application.SessionStateQuery, jid string) (string, error) {
	f.abouts = append(f.abouts, jid)
	return "available", nil
}

func (f *fakeEngine) SetBlocked(_ context.Context, command application.ContactJIDCommand) (application.MutationOnlyResult, error) {
	f.blocks = append(f.blocks, command)
	return application.MutationOnlyResult{MutationResult: application.MutationResult{CommandID: command.CommandID, OrganizationID: command.OrganizationID, SessionID: command.SessionID, GatewayID: command.GatewayID, AssignmentEpoch: command.AssignmentEpoch}}, nil
}

func (f *fakeEngine) MutateGroup(_ context.Context, command application.GroupMutationCommand) (application.GroupCreateResult, error) {
	f.groupCommands = append(f.groupCommands, command)
	result := application.GroupCreateResult{
		MutationOnlyResult: application.MutationOnlyResult{MutationResult: application.MutationResult{CommandID: command.CommandID, OrganizationID: command.OrganizationID, SessionID: command.SessionID, GatewayID: command.GatewayID, AssignmentEpoch: command.AssignmentEpoch}},
	}
	if command.Kind == application.GroupOpCreate {
		result.CreatedGroup = application.GroupInfoResult{GroupJID: "120363@g.us", Subject: command.Name, Participants: int32(len(command.Participants))}
	}
	return result, nil
}

func (f *fakeEngine) GetGroupInviteLink(_ context.Context, _ application.SessionStateQuery, _ string, reset bool) (string, error) {
	f.inviteResets = append(f.inviteResets, reset)
	return "https://chat.whatsapp.com/abc", nil
}

func (f *fakeEngine) JoinGroup(_ context.Context, _ application.SessionStateQuery, invite string) (string, error) {
	f.joins = append(f.joins, invite)
	return "120363@g.us", nil
}

func (f *fakeEngine) GetChatPresence(_ context.Context, _ application.SessionStateQuery, chatJID string) (domain.PresenceStatus, error) {
	f.subscribed = append(f.subscribed, chatJID)
	return domain.PresenceStatus{ChatJID: chatJID, From: chatJID, State: "unknown"}, nil
}

func (f *fakeEngine) SetChatPresence(_ context.Context, command application.ChatPresenceCommand) error {
	f.chatPresence = append(f.chatPresence, command)
	return nil
}

func (f *fakeEngine) BackfillSession(context.Context, application.SessionStateQuery) (domain.BackfillSnapshot, error) {
	f.backfillCalled = true
	return domain.BackfillSnapshot{
		Contacts: []domain.BackfillContact{{LID: "2052@lid", PhoneNumber: "628123"}},
		Groups:   []domain.BackfillGroup{{GroupJID: "120363@g.us", Members: []domain.BackfillMember{{LID: "2052@lid", Role: domain.RoleAdmin}}}},
	}, nil
}

func (f *fakeEngine) PrepareSession(_ context.Context, query application.SessionStateQuery) (application.PrepareSessionResult, error) {
	f.prepared = append(f.prepared, query)
	return application.PrepareSessionResult{MutationResult: application.MutationResult{OrganizationID: query.OrganizationID, SessionID: query.SessionID, GatewayID: query.GatewayID, AssignmentEpoch: query.AssignmentEpoch}}, nil
}

func (f *fakeEngine) BeginPairing(_ context.Context, query application.SessionStateQuery) (application.PairingSnapshot, error) {
	f.beginPair = query
	return f.pairSnap, nil
}

func (f *fakeEngine) PairPhone(_ context.Context, _ application.SessionStateQuery, phone string) (string, error) {
	f.pairPhones = append(f.pairPhones, phone)
	return f.pairingCode, nil
}

func (f *fakeEngine) LogoutSession(_ context.Context, command application.ContactJIDCommand) (application.MutationOnlyResult, error) {
	f.logouts = append(f.logouts, command)
	return application.MutationOnlyResult{MutationResult: application.MutationResult{CommandID: command.CommandID, OrganizationID: command.OrganizationID, SessionID: command.SessionID, GatewayID: command.GatewayID, AssignmentEpoch: command.AssignmentEpoch}}, nil
}

func (f *fakeEngine) ForgetSession(_ context.Context, _, sessionID string) error {
	f.forgotten = append(f.forgotten, sessionID)
	return nil
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
		AssignmentEpoch: 3, CommandId: "cmd_1", PayloadJson: []byte(`{"type":"text","to":"628123@s.whatsapp.net","text":"hi","replyTo":"quoted-id"}`),
		QuoteContext: &gatewayv1.QuotedMessageContext{
			ChatJid: "628123@s.whatsapp.net", SenderJid: "628123@s.whatsapp.net", Type: "text", Body: "original",
		},
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
	quote := engine.sent[0].Payload.QuoteContext
	if quote == nil || quote.SenderJID != "628123@s.whatsapp.net" || quote.Body != "original" || len(engine.sent[0].Payload.Mentions) != 0 {
		t.Fatalf("quote context not carried separately from mentions: %#v", engine.sent[0].Payload)
	}

	_, err = server.SendMessage(context.Background(), &gatewayv1.SendMessageRequest{
		Target:          &gatewayv1.SessionTarget{OrganizationId: "org", SessionId: "s", GatewayId: "gw"},
		AssignmentEpoch: 3, CommandId: "cmd_2", PayloadJson: []byte(`{not-json`),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad json code = %s", status.Code(err))
	}
}

func target(gatewayID string) *gatewayv1.SessionTarget {
	return &gatewayv1.SessionTarget{OrganizationId: "org", SessionId: "s", GatewayId: gatewayID}
}

func TestLiveResourceReadsCarryTargetsAndRejectZeroEpoch(t *testing.T) {
	engine := &fakeEngine{}
	server := &Server{GatewayID: "gw", Engine: engine}

	if _, err := server.LookupContact(context.Background(), &gatewayv1.LookupContactRequest{Target: target("gw"), AssignmentEpoch: 5, Phones: []string{"+628123"}}); err != nil {
		t.Fatalf("LookupContact: %v", err)
	}
	if len(engine.lookups) != 1 || engine.lookups[0].Phones[0] != "+628123" || engine.lookups[0].AssignmentEpoch != 5 {
		t.Fatalf("lookup command = %#v", engine.lookups)
	}
	if _, err := server.GetContactPicture(context.Background(), &gatewayv1.GetContactPictureRequest{Target: target("gw"), AssignmentEpoch: 5, Jid: "628123@s.whatsapp.net"}); err != nil {
		t.Fatalf("GetContactPicture: %v", err)
	}
	if _, err := server.GetContactAbout(context.Background(), &gatewayv1.GetContactAboutRequest{Target: target("gw"), AssignmentEpoch: 5, Jid: "628123@s.whatsapp.net"}); err != nil {
		t.Fatalf("GetContactAbout: %v", err)
	}
	if _, err := server.GetGroupInviteLink(context.Background(), &gatewayv1.GetGroupInviteLinkRequest{Target: target("gw"), AssignmentEpoch: 5, GroupJid: "120363@g.us", Reset_: true}); err != nil {
		t.Fatalf("GetGroupInviteLink: %v", err)
	}
	if !engine.inviteResets[0] {
		t.Fatal("reset flag lost")
	}
	if _, err := server.JoinGroup(context.Background(), &gatewayv1.JoinGroupRequest{Target: target("gw"), AssignmentEpoch: 5, Invite: "abc"}); err != nil {
		t.Fatalf("JoinGroup: %v", err)
	}
	if _, err := server.GetChatPresence(context.Background(), &gatewayv1.GetChatPresenceRequest{Target: target("gw"), AssignmentEpoch: 5, ChatJid: "628123@s.whatsapp.net"}); err != nil {
		t.Fatalf("GetChatPresence: %v", err)
	}
	if len(engine.subscribed) != 1 || engine.subscribed[0] != "628123@s.whatsapp.net" {
		t.Fatalf("subscription = %v", engine.subscribed)
	}

	for name, call := range map[string]func() error{
		"zero epoch lookup": func() error {
			_, err := server.LookupContact(context.Background(), &gatewayv1.LookupContactRequest{Target: target("gw"), Phones: []string{"+62"}})
			return err
		},
		"empty phones": func() error {
			_, err := server.LookupContact(context.Background(), &gatewayv1.LookupContactRequest{Target: target("gw"), AssignmentEpoch: 5})
			return err
		},
		"foreign gateway read": func() error {
			_, err := server.GetContactPicture(context.Background(), &gatewayv1.GetContactPictureRequest{Target: target("other"), AssignmentEpoch: 5, Jid: "j"})
			return err
		},
		"missing chat presence": func() error {
			_, err := server.GetChatPresence(context.Background(), &gatewayv1.GetChatPresenceRequest{Target: target("gw"), AssignmentEpoch: 5})
			return err
		},
		"missing invite jid": func() error {
			_, err := server.GetGroupInviteLink(context.Background(), &gatewayv1.GetGroupInviteLinkRequest{Target: target("gw"), AssignmentEpoch: 5, Reset_: false})
			return err
		},
		"missing join invite": func() error {
			_, err := server.JoinGroup(context.Background(), &gatewayv1.JoinGroupRequest{Target: target("gw"), AssignmentEpoch: 5})
			return err
		},
	} {
		if status.Code(call()) != codes.InvalidArgument {
			t.Fatalf("%s: code = %s, want InvalidArgument", name, status.Code(call()))
		}
	}
}

func TestSetBlockedCarriesCommandAndValidates(t *testing.T) {
	engine := &fakeEngine{}
	server := &Server{GatewayID: "gw", Engine: engine}
	response, err := server.SetBlocked(context.Background(), &gatewayv1.SetBlockedRequest{
		Target: target("gw"), AssignmentEpoch: 7, CommandId: "blk_1", Jid: "628123@s.whatsapp.net", Blocked: true,
	})
	if err != nil {
		t.Fatalf("SetBlocked: %v", err)
	}
	if response.CommandId != "blk_1" || response.AssignmentEpoch != 7 {
		t.Fatalf("response = %#v", response)
	}
	if len(engine.blocks) != 1 || !engine.blocks[0].Blocked || engine.blocks[0].JID != "628123@s.whatsapp.net" {
		t.Fatalf("block command = %#v", engine.blocks)
	}

	_, err = server.SetBlocked(context.Background(), &gatewayv1.SetBlockedRequest{Target: target("gw"), AssignmentEpoch: 7, Jid: "628123@s.whatsapp.net"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing command id code = %s", status.Code(err))
	}
	_, err = server.SetBlocked(context.Background(), &gatewayv1.SetBlockedRequest{Target: target("gw"), AssignmentEpoch: 7, CommandId: "blk_2"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing jid code = %s", status.Code(err))
	}
}

func TestGroupMutationsValidateAndCarryCommands(t *testing.T) {
	engine := &fakeEngine{}
	server := &Server{GatewayID: "gw", Engine: engine}

	create, err := server.CreateGroup(context.Background(), &gatewayv1.CreateGroupRequest{
		Target: target("gw"), AssignmentEpoch: 9, CommandId: "grp_1", Name: "Team", Participants: []string{"628123@s.whatsapp.net"},
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if create.Group == nil || create.Group.GroupJid != "120363@g.us" || create.Group.Subject != "Team" {
		t.Fatalf("create response = %#v", create)
	}

	settingsCmd := &gatewayv1.UpdateGroupSettingsRequest{
		Target: target("gw"), AssignmentEpoch: 9, CommandId: "grp_2", GroupJid: "120363@g.us",
	}
	subject, announce := "NewName", true
	settingsCmd.Subject, settingsCmd.Announce = &subject, &announce
	if _, err := server.UpdateGroupSettings(context.Background(), settingsCmd); err != nil {
		t.Fatalf("UpdateGroupSettings: %v", err)
	}
	participantsCmd := &gatewayv1.UpdateGroupParticipantsRequest{
		Target: target("gw"), AssignmentEpoch: 9, CommandId: "grp_3", GroupJid: "120363@g.us",
		Participants: []string{"628123@s.whatsapp.net"}, Action: gatewayv1.GroupParticipantChange_GROUP_PARTICIPANT_CHANGE_PROMOTE,
	}
	if _, err := server.UpdateGroupParticipants(context.Background(), participantsCmd); err != nil {
		t.Fatalf("UpdateGroupParticipants: %v", err)
	}
	if _, err := server.LeaveGroup(context.Background(), &gatewayv1.LeaveGroupRequest{Target: target("gw"), AssignmentEpoch: 9, CommandId: "grp_4", GroupJid: "120363@g.us"}); err != nil {
		t.Fatalf("LeaveGroup: %v", err)
	}

	if len(engine.groupCommands) != 4 {
		t.Fatalf("group commands = %d", len(engine.groupCommands))
	}
	if engine.groupCommands[1].Settings.Subject == nil || *engine.groupCommands[1].Settings.Subject != "NewName" ||
		engine.groupCommands[1].Settings.Description != nil || engine.groupCommands[1].Settings.Locked != nil {
		t.Fatalf("settings not carried faithfully: %#v", engine.groupCommands[1].Settings)
	}
	if engine.groupCommands[2].Action != application.GroupChangePromote {
		t.Fatalf("action = %q", engine.groupCommands[2].Action)
	}

	for name, call := range map[string]func() error{
		"create missing name": func() error {
			_, err := server.CreateGroup(context.Background(), &gatewayv1.CreateGroupRequest{Target: target("gw"), AssignmentEpoch: 9, CommandId: "x", Name: "", Participants: []string{"j"}})
			return err
		},
		"settings nothing to update": func() error {
			_, err := server.UpdateGroupSettings(context.Background(), &gatewayv1.UpdateGroupSettingsRequest{Target: target("gw"), AssignmentEpoch: 9, CommandId: "x", GroupJid: "g"})
			return err
		},
		"participants unspecified action": func() error {
			_, err := server.UpdateGroupParticipants(context.Background(), &gatewayv1.UpdateGroupParticipantsRequest{Target: target("gw"), AssignmentEpoch: 9, CommandId: "x", GroupJid: "g", Participants: []string{"j"}, Action: gatewayv1.GroupParticipantChange_GROUP_PARTICIPANT_CHANGE_UNSPECIFIED})
			return err
		},
		"leave missing command id": func() error {
			_, err := server.LeaveGroup(context.Background(), &gatewayv1.LeaveGroupRequest{Target: target("gw"), AssignmentEpoch: 9, GroupJid: "g"})
			return err
		},
	} {
		if status.Code(call()) != codes.InvalidArgument {
			t.Fatalf("%s: code = %s, want InvalidArgument", name, status.Code(call()))
		}
	}
}

func TestChatPresenceMutationValidatesState(t *testing.T) {
	engine := &fakeEngine{}
	server := &Server{GatewayID: "gw", Engine: engine}
	if _, err := server.SetChatPresence(context.Background(), &gatewayv1.SetChatPresenceRequest{Target: target("gw"), AssignmentEpoch: 4, ChatJid: "c@s.whatsapp.net", State: "composing"}); err != nil {
		t.Fatalf("SetChatPresence: %v", err)
	}
	if len(engine.chatPresence) != 1 || engine.chatPresence[0].State != "composing" || engine.chatPresence[0].AssignmentEpoch != 4 {
		t.Fatalf("command = %#v", engine.chatPresence)
	}
	if _, err := server.SetChatPresence(context.Background(), &gatewayv1.SetChatPresenceRequest{Target: target("gw"), AssignmentEpoch: 4, ChatJid: "c@s.whatsapp.net", State: "bogus"}); err != nil {
		t.Fatal("state validation must happen in the adapter, not the transport")
	}
}

func TestBackfillSessionReturnsSnapshot(t *testing.T) {
	engine := &fakeEngine{}
	server := &Server{GatewayID: "gw", Engine: engine}
	response, err := server.BackfillSession(context.Background(), &gatewayv1.BackfillSessionRequest{Target: target("gw"), AssignmentEpoch: 11})
	if err != nil {
		t.Fatalf("BackfillSession: %v", err)
	}
	if !engine.backfillCalled {
		t.Fatal("engine was not called")
	}
	snapshot := response.GetSnapshot()
	if len(snapshot.Contacts) != 1 || snapshot.Contacts[0].Lid != "2052@lid" {
		t.Fatalf("contacts = %#v", snapshot.Contacts)
	}
	if len(snapshot.Groups) != 1 || len(snapshot.Groups[0].Members) != 1 || snapshot.Groups[0].Members[0].Role != "admin" {
		t.Fatalf("groups = %#v", snapshot.Groups)
	}

	_, err = server.BackfillSession(context.Background(), &gatewayv1.BackfillSessionRequest{Target: target("gw")})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("zero epoch code = %s", status.Code(err))
	}
}

func TestSessionLifecycleRPCsValidateAndCarryTargets(t *testing.T) {
	engine := &fakeEngine{pairSnap: application.PairingSnapshot{Code: "QR_X", ExpiresAt: 42}, pairingCode: "ABCD-1234"}
	server := &Server{GatewayID: "gw", Engine: engine}

	prepared, err := server.PrepareSession(context.Background(), &gatewayv1.PrepareSessionRequest{Target: target("gw"), AssignmentEpoch: 6})
	if err != nil || prepared.AssignmentEpoch != 6 || len(engine.prepared) != 1 {
		t.Fatalf("PrepareSession = %#v, %v", prepared, err)
	}
	pairing, err := server.BeginPairing(context.Background(), &gatewayv1.BeginPairingRequest{Target: target("gw"), AssignmentEpoch: 6})
	if err != nil || pairing.GetQrCode() != "QR_X" || pairing.GetQrExpiresAtUnixMs() != 42 {
		t.Fatalf("BeginPairing = %#v, %v", pairing, err)
	}
	code, err := server.PairPhone(context.Background(), &gatewayv1.PairPhoneRequest{Target: target("gw"), AssignmentEpoch: 6, Phone: "+628123"})
	if err != nil || code.GetPairingCode() != "ABCD-1234" || engine.pairPhones[0] != "+628123" {
		t.Fatalf("PairPhone = %#v, %v", code, err)
	}
	logout, err := server.LogoutSession(context.Background(), &gatewayv1.LogoutSessionRequest{Target: target("gw"), AssignmentEpoch: 6, CommandId: "out_1"})
	if err != nil || logout.CommandId != "out_1" || logout.AssignmentEpoch != 6 {
		t.Fatalf("LogoutSession = %#v, %v", logout, err)
	}
	forget, err := server.ForgetSession(context.Background(), &gatewayv1.ForgetSessionRequest{Target: target("gw")})
	if err != nil || len(engine.forgotten) != 1 || engine.forgotten[0] != "s" {
		t.Fatalf("ForgetSession = %#v forgotten = %v, %v", forget, engine.forgotten, err)
	}
	if _, err := server.ForgetSession(context.Background(), &gatewayv1.ForgetSessionRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing forget target code = %s", status.Code(err))
	}

	engine.pairSnap = application.PairingSnapshot{}
	bare, err := server.BeginPairing(context.Background(), &gatewayv1.BeginPairingRequest{Target: target("gw"), AssignmentEpoch: 6})
	if err != nil || bare.QrCode != nil || bare.QrExpiresAtUnixMs != nil {
		t.Fatalf("bare BeginPairing = %#v, %v (no-code must omit both fields)", bare, err)
	}

	for name, call := range map[string]func() error{
		"prepare zero epoch": func() error {
			_, err := server.PrepareSession(context.Background(), &gatewayv1.PrepareSessionRequest{Target: target("gw")})
			return err
		},
		"foreign gateway prepare": func() error {
			_, err := server.PrepareSession(context.Background(), &gatewayv1.PrepareSessionRequest{Target: target("other"), AssignmentEpoch: 6})
			return err
		},
		"begin-pairing zero epoch": func() error {
			_, err := server.BeginPairing(context.Background(), &gatewayv1.BeginPairingRequest{Target: target("gw")})
			return err
		},
		"pair-phone missing phone": func() error {
			_, err := server.PairPhone(context.Background(), &gatewayv1.PairPhoneRequest{Target: target("gw"), AssignmentEpoch: 6})
			return err
		},
		"logout missing command id": func() error {
			_, err := server.LogoutSession(context.Background(), &gatewayv1.LogoutSessionRequest{Target: target("gw"), AssignmentEpoch: 6})
			return err
		},
	} {
		if status.Code(call()) != codes.InvalidArgument {
			t.Fatalf("%s: code = %s, want InvalidArgument", name, status.Code(call()))
		}
	}
}
