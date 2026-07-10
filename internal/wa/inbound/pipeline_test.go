package inbound

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
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

// TestProcess_DMCapture processes a direct text message and records the sender identity, chat, and
// message before fan-out. The assertions pin capture order and ensure the emitted event observes the
// persisted canonical data.
func TestProcess_DMCapture(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventMessage)
	f.norm.nm = dmMessage()
	p := f.newPipeline(WithSessionConfig(func(string) (SessionConfig, bool) {
		return SessionConfig{}, true // auto_read off
	}))

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))

	require.Len(t, f.repos.identities, 1)
	assert.Equal(t, "111@lid", f.repos.identities[0].LID)
	assert.Equal(t, "Alice", f.repos.identities[0].Name)

	require.Len(t, f.repos.chats, 1)
	assert.Equal(t, domain.ChatDM, f.repos.chats[0].Type)

	require.Len(t, f.repos.messages, 1)
	m := f.repos.messages[0]
	assert.Equal(t, "MSG1", m.WAMessageID)
	assert.Equal(t, domain.DirectionIn, m.Direction)
	assert.Equal(t, json.RawMessage(`{"k":"v"}`), json.RawMessage(m.RawJSON))

	// no group rows for a DM
	assert.Empty(t, f.repos.groups)
	assert.Empty(t, f.repos.members)
}

// TestProcess_GroupCapture feeds a group message with participant metadata through capture. It
// verifies group, member, identity, and message records are populated consistently before subscribers
// receive the event.
func TestProcess_GroupCapture(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventMessage)
	f.norm.nm = groupMessage()
	p := f.newPipeline()

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))

	require.Len(t, f.repos.groups, 1)
	assert.Equal(t, "12036@g.us", f.repos.groups[0].GroupJID)

	require.Len(t, f.repos.members, 2)
	assert.Equal(t, domain.RoleAdmin, f.repos.members[0].Role)
	assert.Equal(t, "Bobby", f.repos.members[0].Tag)
	assert.Equal(t, domain.RoleMember, f.repos.members[1].Role, "empty role defaults to member")
}

// TestProcess_GroupMentionEventUsesPushNameAndTagMap combines the sender push name with configured
// mention tags for a group message. It checks the emitted mention event and mapped tags, guarding the
// distinction between display-name enrichment and command matching.
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
// TestProcess_ReplyEnrichesQuoteFromStore resolves a reply target from persistence and overlays
// authoritative quote author, body, and from-me fields. The enriched quote must be visible in the
// outgoing event without changing the original message identity.
func TestProcess_ReplyEnrichesQuoteFromStore(t *testing.T) {
	f := newFakes()
	nm := dmMessage()
	nm.QuotedMessageID = "QUOTED1"
	f.norm.evt = domain.NewEvent(domain.EventMessage, testSession, testOrganization, apitypes.MessagePayload{
		WAMessageID:     nm.WAMessageID,
		ChatJID:         nm.ChatJID,
		Type:            nm.MsgType,
		Body:            nm.Body,
		Timestamp:       nm.TimestampMs,
		QuotedMessageID: "QUOTED1",
	})
	f.norm.nm = nm
	f.repos.quotedCtx = map[string]QuotedContext{
		"QUOTED1": {FromMe: true, SenderJID: "628000@s.whatsapp.net", Body: "the quoted text"},
	}
	p := f.newPipeline()

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))

	require.Len(t, f.sink.published, 1)
	payload, ok := f.sink.published[0].Payload.(apitypes.MessagePayload)
	require.True(t, ok)
	assert.True(t, payload.QuotedFromMe)
	assert.Equal(t, "628000@s.whatsapp.net", payload.QuotedSenderJID)
	assert.Equal(t, "the quoted text", payload.QuotedBody)

	// The persisted raw_json mirrors the enriched payload.
	require.Len(t, f.repos.messages, 1)
	var raw struct {
		QuotedFromMe bool   `json:"quotedFromMe"`
		QuotedBody   string `json:"quotedBody"`
	}
	require.NoError(t, json.Unmarshal(f.repos.messages[0].RawJSON, &raw))
	assert.True(t, raw.QuotedFromMe)
	assert.Equal(t, "the quoted text", raw.QuotedBody)
}

// When the quoted message is not in local storage (older than retention), the
// event keeps whatever the protocol frame supplied and quotedFromMe stays false.
// TestProcess_ReplyQuoteNotInStoreKeepsFrameValues simulates a reply whose target has not been
// captured locally. Processing retains the inline WhatsApp frame values and continues, so missing
// history never drops an otherwise valid reply.
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

// TestProcess_LoginInterceptorPassThroughWhenNotMatched uses ordinary text that does not match the
// login command. The message follows normal persistence and fan-out, ensuring the interceptor is
// narrow rather than a blanket direct-message filter.
func TestProcess_LoginInterceptorPassThroughWhenNotMatched(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventMessage)
	f.norm.nm = dmMessage()
	login := &fakeLoginInterceptor{handled: false}
	p := f.newPipeline(WithLoginInterceptor(login))

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))

	require.Equal(t, 1, login.calls)
	assert.Len(t, f.repos.messages, 1)
	assert.Len(t, f.sink.published, 1)
}

// TestProcess_AutoReadBeforeFanout enables automatic receipts for an inbound message and records
// collaborator call order. The read receipt must be attempted after durable processing but before
// external fan-out.
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

// TestProcess_AutoReadDisabled processes the same message with automatic receipts turned off.
// Persistence and fan-out still occur, but the WhatsApp client receives no read call.
func TestProcess_AutoReadDisabled(t *testing.T) {
	tests := []struct {
		name   string
		opts   []Option
		mutate func(*NormalizedMessage)
	}{
		{
			name: "auto_read flag off",
			opts: []Option{WithSessionConfig(func(string) (SessionConfig, bool) {
				return SessionConfig{AutoRead: false}, true
			})},
		},
		{
			name: "no session config resolver", // default: disabled
		},
		{
			name: "unknown session",
			opts: []Option{WithSessionConfig(func(string) (SessionConfig, bool) {
				return SessionConfig{AutoRead: true}, false
			})},
		},
		{
			name:   "outbound echo not read",
			opts:   []Option{WithSessionConfig(func(string) (SessionConfig, bool) { return SessionConfig{AutoRead: true}, true })},
			mutate: func(nm *NormalizedMessage) { nm.FromMe = true },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakes()
			f.norm.evt = event(domain.EventMessage)
			nm := dmMessage()
			if tc.mutate != nil {
				tc.mutate(nm)
			}
			f.norm.nm = nm
			p := f.newPipeline(tc.opts...)

			require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))
			assert.Empty(t, f.wa.readReceipts, "no read receipt expected")
		})
	}
}

// TestProcess_Receipt sends a normalized receipt through the receipt persistence path and then
// fan-out. It verifies status updates use all referenced message IDs while avoiding message capture
// intended only for content events.
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

// TestProcess_NormalizeDrop gives the pipeline an event the normalizer intentionally ignores. It
// returns cleanly with no capture, persistence, WhatsApp, or fan-out calls.
func TestProcess_NormalizeDrop(t *testing.T) {
	f := newFakes()
	f.norm.ok = false
	p := f.newPipeline()

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))
	assert.Empty(t, f.order.snapshot(), "no stage should run on a dropped event")
}

// TestProcess_FromMeEcho feeds a message echoed by the gateway's own account. The pipeline
// reconciles the durable message while skipping inbound-only automation such as command interception
// and auto-read.
func TestProcess_FromMeEcho(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventMessageFromMe)
	nm := dmMessage()
	nm.FromMe = true
	f.norm.nm = nm
	p := f.newPipeline(WithSessionConfig(func(string) (SessionConfig, bool) {
		return SessionConfig{AutoRead: true}, true
	}))

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))

	require.Len(t, f.repos.messages, 1)
	assert.Equal(t, domain.DirectionOut, f.repos.messages[0].Direction)
	assert.Empty(t, f.wa.readReceipts, "echo is not auto-read")
}

// TestProcess_CaptureError injects a failure in the identity/chat capture stage. Processing returns
// that error and does not persist or fan out the message, preventing events from outrunning their
// required durable context.
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

// TestProcess_NoSenderSkipsIdentity processes an event that has no sender address. It bypasses
// identity capture while retaining the remaining applicable persistence and delivery work.
func TestProcess_NoSenderSkipsIdentity(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventChatUpdate)
	f.norm.nm = &NormalizedMessage{
		Kind:    KindOther,
		ChatJID: "12036@g.us",
	}
	p := f.newPipeline()

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))
	assert.Empty(t, f.repos.identities)
	assert.Len(t, f.sink.published, 1)
}

// TestProcess_SystemMessageDropped supplies a normalized system subtype that has no user-visible
// content. The pipeline performs no durable or external side effects, preserving the catalog's
// explicit drop policy.
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

// TestProcess_PushNameFill provides a sender push name absent from the local identity record.
// Capture stores the new display hint and the emitted payload carries it, allowing later reads to
// resolve a useful name.
func TestProcess_PushNameFill(t *testing.T) {
	f := newFakes()
	f.norm.evt = event(domain.EventContactUpdate)
	f.norm.nm = &NormalizedMessage{
		Kind:      KindOther,
		ChatJID:   "628111@s.whatsapp.net",
		SenderJID: "628111@s.whatsapp.net",
		PushName:  "Alice",
	}
	p := f.newPipeline()

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))

	assert.Empty(t, f.repos.identities, "no identity insert from a name hint")
	require.Len(t, f.repos.nameFills, 1)
	assert.Equal(t, "628111@s.whatsapp.net", f.repos.nameFills[0].JID)
	assert.Equal(t, "Alice", f.repos.nameFills[0].Name)
	assert.Len(t, f.sink.published, 1, "contact update still fans out")
}

// TestNoopCommandRegistry exercises the default registry with an arbitrary command candidate. It
// reports no match and no error, providing a safe zero-configuration command boundary.
func TestNoopCommandRegistry(t *testing.T) {
	handled, err := NewNoopCommandRegistry().Handle(context.Background(), "s", "anything")
	require.NoError(t, err)
	assert.False(t, handled)
}

// TestSystemClock samples the production clock around a wall-clock read. The returned millisecond
// value must lie within that interval, guarding unit conversion without assuming exact scheduler
// timing.
func TestSystemClock(t *testing.T) {
	got := SystemClock{}.NowMs()
	assert.Greater(t, got, int64(1_600_000_000_000))
}
