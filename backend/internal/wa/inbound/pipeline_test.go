package inbound

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSession      = "sess_test"
	testOrganization = "ten_test"
)

// dmMessage builds a normalized inbound DM text message.
func dmMessage() *NormalizedMessage {
	return &NormalizedMessage{
		Kind:        KindMessage,
		ChatJID:     "628111@s.whatsapp.net",
		ChatType:    domain.ChatDM,
		ChatName:    "Alice",
		IsDM:        true,
		SenderLID:   "111@lid",
		SenderJID:   "628111@s.whatsapp.net",
		SenderPhone: "628111",
		PushName:    "Alice",
		WAMessageID: "MSG1",
		MsgType:     "text",
		Body:        "hello",
		TimestampMs: 1_699_999_999_000,
		RawJSON:     json.RawMessage(`{"k":"v"}`),
	}
}

// groupMessage builds a normalized inbound group text message with members.
func groupMessage() *NormalizedMessage {
	return &NormalizedMessage{
		Kind:        KindMessage,
		ChatJID:     "12036@g.us",
		ChatType:    domain.ChatGroup,
		ChatName:    "Lunch Crew",
		IsGroup:     true,
		SenderLID:   "222@lid",
		SenderJID:   "628222@s.whatsapp.net",
		PushName:    "Bob",
		WAMessageID: "MSG2",
		MsgType:     "text",
		Body:        "lunch?",
		TimestampMs: 1_699_999_999_500,
		RawJSON:     json.RawMessage(`{}`),
		Group: &NormalizedGroup{
			GroupJID: "12036@g.us",
			Subject:  "Lunch Crew",
		},
		Members: []NormalizedMember{
			{LID: "222@lid", JID: "628222@s.whatsapp.net", Tag: "Bobby", Role: domain.RoleAdmin},
			{LID: "333@lid", JID: "628333@s.whatsapp.net"}, // role defaults to member
		},
	}
}

func event(typ string) domain.Event {
	return domain.NewEvent(typ, testSession, testOrganization, map[string]any{"x": 1})
}

func TestProcess_GroupMentionEventUsesPushNameAndTagMap(t *testing.T) {
	f := newFakes()
	nm := groupMessage()
	nm.Body = "hi @333"
	nm.Mentions = []string{"333@lid"}
	f.norm.evt = domain.NewEvent(domain.EventMessage, testSession, testOrganization, apitypes.MessagePayload{
		WAMessageID: nm.WAMessageID,
		ChatJID:     nm.ChatJID,
		FromMe:      nm.FromMe,
		Type:        nm.MsgType,
		Body:        nm.Body,
		Timestamp:   nm.TimestampMs,
		PushName:    nm.PushName,
	})
	f.norm.nm = nm
	f.repos.mentionDetails = map[string]MentionDetail{
		"333@lid": {PushName: "Carla", Tag: "Caz"},
	}
	p := f.newPipeline()

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))

	require.Len(t, f.repos.messages, 1)
	assert.Equal(t, []string{"333@lid"}, f.repos.messages[0].Mentions)

	require.Len(t, f.sink.published, 1)
	payload, ok := f.sink.published[0].Payload.(apitypes.MessagePayload)
	require.True(t, ok)
	require.Equal(t, map[string]apitypes.MentionData{
		"333@lid": {PushName: "Carla", Tag: "Caz"},
	}, payload.Mentions)

	var raw struct {
		Mentions map[string]apitypes.MentionData `json:"mentions"`
	}
	require.NoError(t, json.Unmarshal(f.repos.messages[0].RawJSON, &raw))
	assert.Equal(t, "Caz", raw.Mentions["333@lid"].Tag)
}

// A reply enriches the event with quotedFromMe resolved from the locally stored
// quoted message, and back-fills the quoted author/body only where the protocol
// frame left them empty (here the frame supplied neither).
func TestProcess_ReplyQuoteNotInStoreKeepsFrameValues(t *testing.T) {
	f := newFakes()
	nm := dmMessage()
	nm.QuotedMessageID = "GONE"
	f.norm.evt = domain.NewEvent(domain.EventMessage, testSession, testOrganization, apitypes.MessagePayload{
		WAMessageID:     nm.WAMessageID,
		ChatJID:         nm.ChatJID,
		Type:            nm.MsgType,
		Timestamp:       nm.TimestampMs,
		QuotedMessageID: "GONE",
		QuotedSenderJID: "628222@s.whatsapp.net", // supplied by the reply frame
		QuotedBody:      "frame body",
	})
	f.norm.nm = nm
	// quotedCtx has no entry for "GONE".
	p := f.newPipeline()

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))

	require.Len(t, f.sink.published, 1)
	payload := f.sink.published[0].Payload.(apitypes.MessagePayload)
	assert.False(t, payload.QuotedFromMe)
	assert.Equal(t, "628222@s.whatsapp.net", payload.QuotedSenderJID)
	assert.Equal(t, "frame body", payload.QuotedBody)
}

// TestProcess_InterceptorDrop installs a matching command interceptor and verifies it stops
// processing after capture. No auto-read or fan-out is allowed once the interceptor claims the
// message, preserving single-consumer command semantics.
func TestProcess_InterceptorDrop(t *testing.T) {
	tests := []struct {
		name           string
		isAdmin        bool
		prefix         string
		body           string
		fromMe         bool
		kind           MessageKind
		wantDropped    bool
		wantCmdHandled bool
	}{
		{
			name: "admin prefixed text is dropped", isAdmin: true, prefix: "am",
			body: "amlogin 123456", kind: KindMessage,
			wantDropped: true, wantCmdHandled: true,
		},
		{
			name: "admin non-prefixed text is processed", isAdmin: true, prefix: "am",
			body: "hello there", kind: KindMessage,
			wantDropped: false, wantCmdHandled: false,
		},
		{
			name: "non-admin prefixed text is processed", isAdmin: false, prefix: "am",
			body: "amlogin 123456", kind: KindMessage,
			wantDropped: false, wantCmdHandled: false,
		},
		{
			name: "admin echo prefixed is processed (not a command)", isAdmin: true, prefix: "am",
			body: "amlogin 1", fromMe: true, kind: KindMessage,
			wantDropped: false, wantCmdHandled: false,
		},
		{
			name: "empty prefix disables interceptor", isAdmin: true, prefix: "",
			body: "amlogin 1", kind: KindMessage,
			wantDropped: false, wantCmdHandled: false,
		},
		{
			name: "admin receipt is never intercepted", isAdmin: true, prefix: "am",
			body: "", kind: KindReceipt,
			wantDropped: false, wantCmdHandled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakes()
			f.norm.evt = event(domain.EventMessage)
			nm := dmMessage()
			nm.Kind = tc.kind
			nm.Body = tc.body
			nm.FromMe = tc.fromMe
			if tc.kind == KindReceipt {
				nm.Receipt = &NormalizedReceipt{MessageIDs: []string{"MSG1"}, Status: domain.MessageRead}
			}
			f.norm.nm = nm
			p := f.newPipeline(WithCommandPrefix(tc.prefix))

			require.NoError(t, p.Process(context.Background(), testSession, testOrganization, tc.isAdmin, struct{}{}))

			if tc.wantCmdHandled {
				require.Len(t, f.commands.calls, 1)
				assert.Equal(t, tc.body, f.commands.calls[0])
			} else {
				assert.Empty(t, f.commands.calls)
			}

			if tc.wantDropped {
				// Nothing persisted/emitted/counted.
				assert.Empty(t, f.repos.identities, "dropped: no identity")
				assert.Empty(t, f.repos.messages, "dropped: not persisted")
				assert.Empty(t, f.repos.eventLog, "dropped: no event_log")
				assert.Empty(t, f.sink.published, "dropped: not emitted")
				assert.Empty(t, f.webhooks.enqueued, "dropped: no webhook")
			} else {
				// Processed: fanned out at minimum.
				assert.Len(t, f.sink.published, 1, "processed: emitted")
				assert.Len(t, f.repos.eventLog, 1, "processed: event_log appended")
			}
		})
	}
}

// TestProcess_InterceptorRegistryError makes command lookup fail before an interceptor can run. The
// error is returned and later pipeline stages remain untouched, preventing partial delivery when
// command routing is unavailable.
func TestProcess_InterceptorRegistryError(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventMessage)
	nm := dmMessage()
	nm.Body = "amfail"
	f.norm.nm = nm
	f.commands.err = errors.New("boom")
	p := f.newPipeline(WithCommandPrefix("am"))

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, true, struct{}{}))
	assert.Empty(t, f.repos.messages, "registry error still drops the event")
	assert.Empty(t, f.sink.published)
}

// TestProcess_LoginInterceptorDropsBeforePersistAndFanout sends a matching login command through
// the built-in interceptor. It verifies the credential exchange consumes the command before message
// persistence and external fan-out can expose it.
func TestProcess_LoginInterceptorDropsBeforePersistAndFanout(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventMessage)
	nm := dmMessage()
	nm.Body = "login 483920"
	f.norm.nm = nm
	login := &fakeLoginInterceptor{handled: true}
	p := f.newPipeline(WithLoginInterceptor(login))

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))

	require.Equal(t, 1, login.calls)
	assert.Empty(t, f.repos.identities)
	assert.Empty(t, f.repos.messages)
	assert.Empty(t, f.repos.eventLog)
	assert.Empty(t, f.sink.published)
	assert.Empty(t, f.webhooks.enqueued)
}

func TestProcess_AutoReadBeforeFanout(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventMessage)
	f.norm.nm = dmMessage()
	p := f.newPipeline(WithSessionConfig(func(string) (SessionConfig, bool) {
		return SessionConfig{AutoRead: true, PresenceTyping: true}, true
	}))

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))

	require.Len(t, f.wa.readReceipts, 1)
	assert.Equal(t, []string{"MSG1"}, f.wa.readReceipts[0].messageIDs)
	assert.Equal(t, []string{presenceComposing}, f.wa.presence)

	steps := f.order.snapshot()
	rr := indexOf(steps, "SendReadReceipt")
	pub := indexOf(steps, "Publish")
	log := indexOf(steps, "AppendEventLog")
	enq := indexOf(steps, "Enqueue")
	require.NotEqual(t, -1, rr)
	require.NotEqual(t, -1, pub)
	assert.Less(t, rr, pub, "read receipt before publish")
	assert.Less(t, rr, log, "read receipt before event_log")
	assert.Less(t, rr, enq, "read receipt before webhook enqueue")

	// And persistence happened before auto-read.
	assert.Less(t, indexOf(steps, "InsertMessage"), rr, "persist before auto-read")
}

func TestProcess_Receipt(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventMessageStatus)
	ack := 3
	f.norm.nm = &NormalizedMessage{
		Kind:      KindReceipt,
		ChatJID:   "628111@s.whatsapp.net",
		SenderLID: "111@lid",
		Receipt: &NormalizedReceipt{
			MessageIDs: []string{"MSG1", "MSG2"},
			Status:     domain.MessageDelivered,
			AckLevel:   &ack,
		},
	}
	p := f.newPipeline(WithSessionConfig(func(string) (SessionConfig, bool) {
		return SessionConfig{AutoRead: true}, true
	}))

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))

	require.Len(t, f.repos.statusUpd, 1)
	upd := f.repos.statusUpd[0]
	assert.Equal(t, []string{"MSG1", "MSG2"}, upd.WAMessageIDs)
	assert.Equal(t, domain.MessageDelivered, upd.Status)
	require.NotNil(t, upd.AckLevel)
	assert.Equal(t, 3, *upd.AckLevel)

	assert.Empty(t, f.repos.messages, "receipt inserts no message")
	assert.Empty(t, f.wa.readReceipts, "receipt does not auto-read")
	// still fanned out
	assert.Len(t, f.sink.published, 1)
}

// TestProcess_PollVote processes a poll response with its target message and selected options. The
// vote-specific persistence call precedes event delivery, keeping emitted poll state backed by durable
// data.
func TestProcess_PollVote(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventPollVote)
	opts := json.RawMessage(`["Pizza"]`)
	f.norm.nm = &NormalizedMessage{
		Kind:      KindPollVote,
		ChatJID:   "12036@g.us",
		SenderLID: "222@lid",
		RawJSON:   json.RawMessage(`{"poll":1}`),
		PollVote: &NormalizedPollVote{
			PollMessageID:   "POLL1",
			VoterLID:        "222@lid",
			SelectedOptions: opts,
			TimestampMs:     1_699_999_000_000,
		},
	}
	p := f.newPipeline()

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))

	require.Len(t, f.repos.pollVotes, 1)
	pv := f.repos.pollVotes[0]
	assert.Equal(t, "POLL1", pv.PollMessageID)
	assert.Equal(t, "222@lid", pv.VoterLID)
	assert.Equal(t, json.RawMessage(opts), json.RawMessage(pv.SelectedOptions))
	assert.Equal(t, json.RawMessage(`{"poll":1}`), json.RawMessage(pv.RawJSON))

	assert.Empty(t, f.repos.messages, "poll vote inserts no message row")
	assert.Len(t, f.sink.published, 1)
}

// TestProcess_EditRevoke covers both protocol edits and revocations against an existing message ID.
// Each case invokes the corresponding mutation and emits the matching catalog event without inserting
// a second message.
func TestProcess_EditRevoke(t *testing.T) {
	t.Run("edit", func(t *testing.T) {
		f := newFakes()
		f.norm.evt = event(domain.EventMessageEdited)
		f.norm.nm = &NormalizedMessage{Kind: KindEdit, WAMessageID: "MSG1", Body: "edited text"}
		p := f.newPipeline()
		require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))
		assert.Equal(t, []string{"MSG1"}, f.repos.edited)
		assert.Empty(t, f.repos.messages)
	})
	t.Run("revoke", func(t *testing.T) {
		f := newFakes()
		f.norm.evt = event(domain.EventMessageRevoked)
		f.norm.nm = &NormalizedMessage{Kind: KindRevoke, WAMessageID: "MSG1"}
		p := f.newPipeline()
		require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))
		assert.Equal(t, []string{"MSG1"}, f.repos.deleted)
	})
}

func TestProcess_CaptureError(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventMessage)
	f.norm.nm = dmMessage()
	f.repos.failOn = "UpsertIdentity"
	f.repos.failErr = errors.New("db down")
	p := f.newPipeline()

	err := p.Process(context.Background(), testSession, testOrganization, false, struct{}{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "inbound capture")
	assert.Empty(t, f.repos.messages, "persist must not run after capture error")
	assert.Empty(t, f.sink.published, "fanout must not run after capture error")
}

// TestProcess_FanoutErrorsJoined makes multiple independent fan-out sinks fail for one persisted
// event. The returned error retains every sink failure, proving one failing destination neither masks
// nor prevents attempts to the others.
func TestProcess_FanoutErrorsJoined(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventMessage)
	f.norm.nm = dmMessage()
	f.sink.err = errors.New("publish fail")
	f.webhooks.err = errors.New("enqueue fail")
	p := f.newPipeline()

	err := p.Process(context.Background(), testSession, testOrganization, false, struct{}{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "inbound fanout")
	// event_log still appended despite the other two failing
	assert.Len(t, f.repos.eventLog, 1)
}

// TestProcess_AutoReadErrorNonFatal makes the WhatsApp read-receipt call fail after persistence.
// Fan-out still succeeds and processing returns no fatal error because auto-read is explicitly best
// effort.
func TestProcess_AutoReadErrorNonFatal(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventMessage)
	f.norm.nm = dmMessage()
	f.wa.readErr = errors.New("offline")
	p := f.newPipeline(WithSessionConfig(func(string) (SessionConfig, bool) {
		return SessionConfig{AutoRead: true}, true
	}))

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))
	assert.Len(t, f.sink.published, 1, "fan-out proceeds despite read-receipt failure")
}

func TestProcess_SystemMessageDropped(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventMessage)
	nm := dmMessage()
	nm.MsgType = "system"
	nm.Body = ""
	f.norm.nm = nm
	p := f.newPipeline()

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))

	require.Len(t, f.repos.identities, 1, "identity still captured")
	assert.Empty(t, f.repos.chats, "no chat upsert for a dropped system message")
	assert.Empty(t, f.repos.messages, "system message not persisted")
	assert.Empty(t, f.sink.published, "system message not fanned out")
	assert.Empty(t, f.repos.eventLog, "system message not logged")
}
