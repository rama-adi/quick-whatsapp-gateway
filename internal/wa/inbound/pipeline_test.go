package inbound

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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

// TestProcess_DMCapture verifies a DM message captures the identity and persists
// chat + message; no group rows (DM "found" is later derived from the chat).
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

// TestProcess_GroupCapture verifies a group message captures group + members
// (with default-member role fill).
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

// TestProcess_InterceptorDrop verifies that a prefixed text on the admin session
// is handed to the registry and dropped: nothing persisted/emitted/counted.
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

// TestProcess_InterceptorRegistryError verifies a registry error still drops the
// event (a prefixed admin message is never a normal message) and does not abort.
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

// TestProcess_AutoReadBeforeFanout verifies the ordering guarantee: when
// auto_read is on, the read receipt is sent strictly before any fan-out.
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

// TestProcess_AutoReadDisabled covers the off paths: disabled flag, no resolver,
// unknown session, outbound echo, and non-message kinds — no read receipt.
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

// TestProcess_Receipt verifies the receipt path updates message status/ack and
// does not insert a message or auto-read.
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

// TestProcess_PollVote verifies the poll-update path inserts a poll_votes row.
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

// TestProcess_EditRevoke verifies edit/revoke flip flags on the target message.
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

// TestProcess_NormalizeDrop verifies an un-ok normalize result is silently
// dropped: no stages run.
func TestProcess_NormalizeDrop(t *testing.T) {
	f := newFakes()
	f.norm.ok = false
	p := f.newPipeline()

	require.NoError(t, p.Process(context.Background(), testSession, testOrganization, false, struct{}{}))
	assert.Empty(t, f.order.snapshot(), "no stage should run on a dropped event")
}

// TestProcess_FromMeEcho verifies an own-number echo is persisted as direction
// out and is not auto-read.
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

// TestProcess_CaptureError verifies a capture-stage error aborts before persist.
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

// TestProcess_FanoutErrorsJoined verifies fan-out failures are joined and
// returned, but all three sinks are still attempted (event_log appended even
// when publish/enqueue fail).
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

// TestProcess_AutoReadErrorNonFatal verifies a read-receipt failure does not
// abort fan-out (auto-read is best-effort).
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

// TestProcess_NoSenderSkipsIdentity verifies events without sender info still
// persist/fan-out but skip identity capture.
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

// TestProcess_SystemMessageDropped verifies a content-less system message still
// captures the sender's identity but is not persisted to messages nor fanned out.
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

// TestProcess_PushNameFill verifies a sender-less push-name sighting (a
// contact.update / push-name event) fills an existing identity's name via
// FillIdentityName instead of upserting a new identity, and still fans out.
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

// TestNoopCommandRegistry confirms the v1 registry recognizes nothing.
func TestNoopCommandRegistry(t *testing.T) {
	handled, err := NewNoopCommandRegistry().Handle(context.Background(), "s", "anything")
	require.NoError(t, err)
	assert.False(t, handled)
}

// TestSystemClock sanity-checks the production clock returns a plausible epoch-ms.
func TestSystemClock(t *testing.T) {
	got := SystemClock{}.NowMs()
	assert.Greater(t, got, int64(1_600_000_000_000))
}
