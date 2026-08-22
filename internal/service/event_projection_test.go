package service

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
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
		domain.EventSessionStatus, domain.EventAuthQR, domain.EventAuthCode,
		domain.EventPresenceUpdate, domain.EventGroupUpdate, domain.EventChatUpdate,
		domain.EventContactUpdate, domain.EventCallIncoming, domain.EventNewsletterUpdate,
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
