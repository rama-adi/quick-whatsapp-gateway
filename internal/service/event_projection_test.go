package service

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

// fakeProjectionStore records every projection write so each consumer branch
// can be asserted without MySQL.
type fakeProjectionStore struct {
	upsertIdentities  []ProjectionIdentityUpsert
	fillNames         [][3]string // jid, name, nowMs stringified by caller
	upsertChats       []ProjectionChatUpsert
	insertedMessages  []ProjectionMessageInsert
	markEdited        [][3]string
	markDeleted       [][2]string
	statusUpdates     []ProjectionMessageStatusUpdate
	upsertedPolls     []ProjectionPollUpsert
	insertedPollVotes []ProjectionPollVoteInsert
	upsertMembers     []ProjectionGroupMemberUpsert
	attachedPairings  []store.AttachPairingInput
	clearedSessions   []string
	clearedAt         []int64
	sessionStatuses   []domain.SessionStatus
}

func (f *fakeProjectionStore) AttachPairing(_ context.Context, in store.AttachPairingInput) error {
	f.attachedPairings = append(f.attachedPairings, in)
	return nil
}

func (f *fakeProjectionStore) ClearPairing(_ context.Context, sessionID string, updatedAt int64) error {
	f.clearedSessions = append(f.clearedSessions, sessionID)
	f.clearedAt = append(f.clearedAt, updatedAt)
	return nil
}

func (f *fakeProjectionStore) UpsertIdentity(_ context.Context, in ProjectionIdentityUpsert) error {
	f.upsertIdentities = append(f.upsertIdentities, in)
	return nil
}

func (f *fakeProjectionStore) FillIdentityName(_ context.Context, jid, name string, nowMs int64) error {
	raw, _ := json.Marshal([]string{jid, name})
	_ = raw
	f.fillNames = append(f.fillNames, [3]string{jid, name})
	return nil
}

func (f *fakeProjectionStore) UpsertGroup(context.Context, ProjectionGroupUpsert) error { return nil }

func (f *fakeProjectionStore) UpsertGroupMember(_ context.Context, in ProjectionGroupMemberUpsert) error {
	f.upsertMembers = append(f.upsertMembers, in)
	return nil
}

func (f *fakeProjectionStore) UpsertChat(_ context.Context, in ProjectionChatUpsert) error {
	f.upsertChats = append(f.upsertChats, in)
	return nil
}

func (f *fakeProjectionStore) InsertMessage(_ context.Context, in ProjectionMessageInsert) error {
	f.insertedMessages = append(f.insertedMessages, in)
	return nil
}

func (f *fakeProjectionStore) MarkMessageEdited(_ context.Context, sessionID, waMessageID, newBody string) error {
	f.markEdited = append(f.markEdited, [3]string{sessionID, waMessageID, newBody})
	return nil
}

func (f *fakeProjectionStore) MarkMessageDeleted(_ context.Context, sessionID, waMessageID string) error {
	f.markDeleted = append(f.markDeleted, [2]string{sessionID, waMessageID})
	return nil
}

func (f *fakeProjectionStore) UpdateMessageStatus(_ context.Context, in ProjectionMessageStatusUpdate) error {
	f.statusUpdates = append(f.statusUpdates, in)
	return nil
}

func (f *fakeProjectionStore) UpsertPoll(_ context.Context, in ProjectionPollUpsert) error {
	f.upsertedPolls = append(f.upsertedPolls, in)
	return nil
}

func (f *fakeProjectionStore) InsertPollVote(_ context.Context, in ProjectionPollVoteInsert) error {
	f.insertedPollVotes = append(f.insertedPollVotes, in)
	return nil
}

func newTestProjectionConsumer(store *fakeProjectionStore) *EventProjectionConsumer {
	return NewEventProjectionConsumer(store, func() int64 { return 1234 })
}

func messageEvent(eventType string, payload apitypes.MessagePayload) domain.Event {
	return domain.Event{Schema: domain.Schema, ID: "evt_1", Type: eventType,
		Session: "sess_1", Organization: "org_1", Timestamp: 1000, Payload: payload}
}

func TestProjectionMessageInsertsChatBeforeMessageAndCapturesSender(t *testing.T) {
	store := &fakeProjectionStore{}
	consumer := newTestProjectionConsumer(store)
	event := messageEvent(domain.EventMessage, apitypes.MessagePayload{
		WAMessageID: "wamid_1", ChatJID: "628123@s.whatsapp.net", SenderLID: "205227043110953@lid",
		PushName: "Alice", Type: "text", Body: "Hello!", Timestamp: 999,
	})

	if err := consumer.ConsumeCommittedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.upsertChats) != 1 || len(store.insertedMessages) != 1 || len(store.upsertIdentities) != 1 {
		t.Fatalf("chat=%d message=%d identity=%d",
			len(store.upsertChats), len(store.insertedMessages), len(store.upsertIdentities))
	}
	if store.upsertChats[0].Type != domain.ChatDM || store.upsertChats[0].Name != "Alice" || store.upsertChats[0].LastMessageAt != 999 {
		t.Fatalf("chat upsert = %+v", store.upsertChats[0])
	}
	msg := store.insertedMessages[0]
	if msg.Direction != domain.DirectionIn || msg.Body != "Hello!" || msg.SenderLID != "205227043110953@lid" {
		t.Fatalf("message insert = %+v", msg)
	}
	if msg.RawJSON == nil {
		t.Fatal("raw_json not persisted")
	}
	if got := store.upsertIdentities[0]; got.LID != "205227043110953@lid" || got.Name != "Alice" {
		t.Fatalf("identity upsert = %+v", got)
	}
}

func TestProjectionFromMeMessageMarksOutboundDirection(t *testing.T) {
	store := &fakeProjectionStore{}
	consumer := newTestProjectionConsumer(store)
	event := messageEvent(domain.EventMessageFromMe, apitypes.MessagePayload{
		WAMessageID: "wamid_2", ChatJID: "120363@g.us", FromMe: true, Type: "text", Body: "hi", Timestamp: 5,
	})
	if err := consumer.ConsumeCommittedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.insertedMessages) != 1 || store.insertedMessages[0].Direction != domain.DirectionOut {
		t.Fatalf("inserts = %+v", store.insertedMessages)
	}
	// Group chats never take the sender's push name as the chat name.
	if len(store.upsertChats) != 1 || store.upsertChats[0].Name != "" || store.upsertChats[0].Type != domain.ChatGroup {
		t.Fatalf("group chat upsert = %+v", store.upsertChats)
	}
	// The from_me sender has no LID; no identity or membership rows are written.
	if len(store.upsertIdentities) != 0 || len(store.upsertMembers) != 0 {
		t.Fatalf("unexpected captures: %+v %+v", store.upsertIdentities, store.upsertMembers)
	}
}

func TestProjectionPollCreationUpsertsPollMetadata(t *testing.T) {
	store := &fakeProjectionStore{}
	consumer := newTestProjectionConsumer(store)
	event := messageEvent(domain.EventMessage, apitypes.MessagePayload{
		WAMessageID: "wamid_3", ChatJID: "628123@s.whatsapp.net", Type: "poll",
		Timestamp: 10, Poll: &apitypes.PollData{Name: "Lunch?", Options: []string{"A", "B"}, SelectableCount: 1},
	})
	if err := consumer.ConsumeCommittedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.upsertedPolls) != 1 || !reflect.DeepEqual(store.upsertedPolls[0].Options, []string{"A", "B"}) {
		t.Fatalf("poll upsert = %+v", store.upsertedPolls)
	}
}

func TestProjectionEditAndRevokeTargetExistingMessages(t *testing.T) {
	store := &fakeProjectionStore{}
	consumer := newTestProjectionConsumer(store)

	edit := messageEvent(domain.EventMessageEdited, apitypes.MessagePayload{
		WAMessageID: "wamid_x", TargetID: "wamid_orig", ChatJID: "628123@s.whatsapp.net", Type: "edit", Body: "new text",
	})
	if err := consumer.ConsumeCommittedEvent(context.Background(), edit); err != nil {
		t.Fatal(err)
	}
	revoke := messageEvent(domain.EventMessageRevoked, apitypes.MessagePayload{
		WAMessageID: "wamid_y", TargetID: "wamid_orig", ChatJID: "628123@s.whatsapp.net", Type: "revoke",
	})
	if err := consumer.ConsumeCommittedEvent(context.Background(), revoke); err != nil {
		t.Fatal(err)
	}
	if len(store.markEdited) != 1 || store.markEdited[0] != [3]string{"sess_1", "wamid_orig", "new text"} {
		t.Fatalf("mark edited = %+v", store.markEdited)
	}
	if len(store.markDeleted) != 1 || store.markDeleted[0] != [2]string{"sess_1", "wamid_orig"} {
		t.Fatalf("mark deleted = %+v", store.markDeleted)
	}
	if len(store.insertedMessages) != 0 {
		t.Fatalf("edit/revoke inserted message rows: %+v", store.insertedMessages)
	}
}

func TestProjectionReceiptAdvancesStatusPerMessage(t *testing.T) {
	store := &fakeProjectionStore{}
	consumer := newTestProjectionConsumer(store)
	event := domain.Event{Schema: domain.Schema, ID: "evt_rcpt", Type: domain.EventMessageStatus,
		Session: "sess_1", Organization: "org_1", Timestamp: 2000,
		Payload: apitypes.MessageStatusPayload{
			ChatJID: "628123@s.whatsapp.net", MessageIDs: []string{"m1", "m2"}, Status: "delivered", Timestamp: 1999,
		}}
	if err := consumer.ConsumeCommittedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.statusUpdates) != 1 || len(store.statusUpdates[0].WAMessageIDs) != 2 ||
		store.statusUpdates[0].Status != domain.MessageDelivered {
		t.Fatalf("status update = %+v", store.statusUpdates)
	}
	if len(store.insertedMessages) != 0 || len(store.upsertChats) != 0 {
		t.Fatalf("receipt must not insert rows: %+v %+v", store.insertedMessages, store.upsertChats)
	}
}

func TestProjectionPollVoteRecordsSelection(t *testing.T) {
	store := &fakeProjectionStore{}
	consumer := newTestProjectionConsumer(store)
	event := messageEvent(domain.EventPollVote, apitypes.MessagePayload{
		WAMessageID: "wamid_v", ChatJID: "628123@s.whatsapp.net", SenderLID: "voter@lid",
		Type: "poll_vote", TargetID: "wamid_poll", SelectedOptions: []string{"Yes"}, Timestamp: 77,
	})
	if err := consumer.ConsumeCommittedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.insertedPollVotes) != 1 {
		t.Fatalf("poll votes = %+v", store.insertedPollVotes)
	}
	vote := store.insertedPollVotes[0]
	if vote.PollMessageID != "wamid_poll" || vote.VoterLID != "voter@lid" {
		t.Fatalf("vote insert = %+v", vote)
	}
	var selected []string
	if err := json.Unmarshal(vote.SelectedOptions, &selected); err != nil || !reflect.DeepEqual(selected, []string{"Yes"}) {
		t.Fatalf("selected options = %s (%v)", vote.SelectedOptions, err)
	}
}

func TestProjectionReactionCapturesSenderWithoutRows(t *testing.T) {
	store := &fakeProjectionStore{}
	consumer := newTestProjectionConsumer(store)
	event := messageEvent(domain.EventMessageReaction, apitypes.MessagePayload{
		WAMessageID: "wamid_r", ChatJID: "628123@s.whatsapp.net", SenderLID: "reactor@lid",
		Type: "reaction", Reaction: "👍", TargetID: "wamid_orig",
	})
	if err := consumer.ConsumeCommittedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.insertedMessages) != 0 || len(store.upsertIdentities) != 1 {
		t.Fatalf("reaction wrote wrong rows: messages=%d identities=%d",
			len(store.insertedMessages), len(store.upsertIdentities))
	}
}

func TestProjectionIgnoresNonProjectedEvents(t *testing.T) {
	store := &fakeProjectionStore{}
	consumer := newTestProjectionConsumer(store)
	for _, eventType := range []string{
		domain.EventAuthQR, domain.EventPresenceUpdate, domain.EventGroupUpdate,
		domain.EventChatUpdate, domain.EventContactUpdate, domain.EventCallIncoming,
		domain.EventNewsletterUpdate,
	} {
		event := domain.Event{Schema: domain.Schema, ID: "evt_" + eventType, Type: eventType,
			Session: "sess_1", Organization: "org_1", Timestamp: 1, Payload: map[string]any{"x": 1}}
		if err := consumer.ConsumeCommittedEvent(context.Background(), event); err != nil {
			t.Fatalf("%s: %v", eventType, err)
		}
	}
	if len(store.upsertChats)+len(store.insertedMessages)+len(store.upsertIdentities)+len(store.insertedPollVotes) != 0 {
		t.Fatal("non-projected events wrote rows")
	}
}

// TestProjectionPairSuccessAttachesIdentity consumes an auth.code event (the
// PairSuccess signal) carrying the linked JIDs. The session row must gain the
// device JID, LID, and derived phone number — desired-state reconciliation
// derives each assignment's DeviceJID from wa_jid, so this projection is what
// lets a freshly paired session start.
func TestProjectionPairSuccessAttachesIdentity(t *testing.T) {
	store := &fakeProjectionStore{}
	consumer := newTestProjectionConsumer(store)
	event := domain.Event{Schema: domain.Schema, ID: "evt_pair", Type: domain.EventAuthCode,
		Session: "sess_1", Organization: "org_1", Timestamp: 1000,
		Payload: apitypes.AuthCodePayload{JID: "628111:7@s.whatsapp.net", LID: "777@lid"}}

	if err := consumer.ConsumeCommittedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.attachedPairings) != 1 {
		t.Fatalf("expected 1 pairing attach, got %d", len(store.attachedPairings))
	}
	got := store.attachedPairings[0]
	if got.SessionID != "sess_1" || got.WaJID != "628111:7@s.whatsapp.net" ||
		got.WaLID != "777@lid" || got.PhoneNumber != "628111" || got.UpdatedAt != 1234 {
		t.Fatalf("pairing attach = %+v", got)
	}
}

// TestProjectionPairSuccessWithoutJIDIsNoOp covers the phone-number pairing
// kickoff variant of auth.code (code only, no identity yet): nothing attaches.
func TestProjectionPairSuccessWithoutJIDIsNoOp(t *testing.T) {
	store := &fakeProjectionStore{}
	consumer := newTestProjectionConsumer(store)
	event := domain.Event{Schema: domain.Schema, ID: "evt_code", Type: domain.EventAuthCode,
		Session: "sess_1", Organization: "org_1", Timestamp: 1000,
		Payload: apitypes.AuthCodePayload{}}

	if err := consumer.ConsumeCommittedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.attachedPairings) != 0 {
		t.Fatalf("identity attached without a JID: %+v", store.attachedPairings)
	}
}

// TestProjectionLoggedOutClearsPairing consumes the terminal logged_out status
// event. The row's WhatsApp identity is cleared so QR/pairing-code preconditions
// immediately see an unpaired session; other statuses update state without clearing identity.
func TestProjectionLoggedOutClearsPairing(t *testing.T) {
	store := &fakeProjectionStore{}
	consumer := newTestProjectionConsumer(store)

	for _, status := range []string{"working", "starting", "scan_qr_code", "failed", "stopped"} {
		event := domain.Event{Schema: domain.Schema, ID: "evt_" + status, Type: domain.EventSessionStatus,
			Session: "sess_1", Organization: "org_1", Timestamp: 1000,
			Payload: apitypes.SessionStatusPayload{Status: status}}
		if err := consumer.ConsumeCommittedEvent(context.Background(), event); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
	}
	if len(store.sessionStatuses) != 5 {
		t.Fatalf("observed statuses not persisted: %v", store.sessionStatuses)
	}
	if !reflect.DeepEqual(store.sessionStatuses, []domain.SessionStatus{domain.SessionWorking, domain.SessionStarting, domain.SessionScanQR, domain.SessionFailed, domain.SessionStopped}) {
		t.Fatalf("wrong observed statuses: %v", store.sessionStatuses)
	}
	if len(store.clearedSessions) != 0 {
		t.Fatalf("non-terminal statuses cleared pairing: %v", store.clearedSessions)
	}

	loggedOut := domain.Event{Schema: domain.Schema, ID: "evt_lo", Type: domain.EventSessionStatus,
		Session: "sess_1", Organization: "org_1", Timestamp: 2000,
		Payload: apitypes.SessionStatusPayload{Status: string(domain.SessionLoggedOut)}}
	if err := consumer.ConsumeCommittedEvent(context.Background(), loggedOut); err != nil {
		t.Fatal(err)
	}
	if len(store.clearedSessions) != 1 || store.clearedSessions[0] != "sess_1" || store.clearedAt[0] != 1234 {
		t.Fatalf("logged-out clear = %v @ %v", store.clearedSessions, store.clearedAt)
	}

	// A replayed or repeated logout clears again (repair semantics).
	if err := consumer.ConsumeCommittedEvent(context.Background(), loggedOut); err != nil {
		t.Fatal(err)
	}
	if len(store.clearedSessions) != 2 {
		t.Fatalf("repeat clear did not run: %v", store.clearedSessions)
	}
}

func (f *fakeProjectionStore) UpdateSessionStatus(_ context.Context, sessionID string, status domain.SessionStatus, updatedAt int64) error {
	f.sessionStatuses = append(f.sessionStatuses, status)
	return nil
}
