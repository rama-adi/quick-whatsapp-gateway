package events

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

const (
	testSession      = "sess_test"
	testOrganization = "ten_test"
)

func mustJID(t *testing.T, s string) types.JID {
	t.Helper()
	j, err := types.ParseJID(s)
	if err != nil {
		t.Fatalf("ParseJID(%q): %v", s, err)
	}
	return j
}

// msgEvent builds an *events.Message with the given content and a DM chat unless
// overridden by the chat argument.
func msgEvent(chat, sender string, fromMe bool, content *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     types.NewJID(splitUser(chat), splitServer(chat)),
				Sender:   types.NewJID(splitUser(sender), splitServer(sender)),
				IsFromMe: fromMe,
			},
			ID:        "wamid.TEST",
			PushName:  "Alice",
			Timestamp: time.UnixMilli(1719400000000),
		},
		Message: content,
	}
}

func splitUser(jid string) string {
	for i := 0; i < len(jid); i++ {
		if jid[i] == '@' {
			return jid[:i]
		}
	}
	return jid
}

func splitServer(jid string) string {
	for i := 0; i < len(jid); i++ {
		if jid[i] == '@' {
			return jid[i+1:]
		}
	}
	return types.DefaultUserServer
}

func TestNormalizeMessageSubtypes(t *testing.T) {
	dm := "628111@s.whatsapp.net"
	group := "12036@g.us"

	tests := []struct {
		name        string
		chat        string
		fromMe      bool
		content     *waE2E.Message
		wantEvent   string
		wantKind    PersistKind
		wantSubtype MessageSubtype
		wantType    string
		check       func(t *testing.T, nm *NormalizedMessage, p MessagePayload)
	}{
		{
			name:        "plain text dm",
			chat:        dm,
			content:     &waE2E.Message{Conversation: proto.String("hello world")},
			wantEvent:   domain.EventMessage,
			wantKind:    PersistMessage,
			wantSubtype: SubtypeText,
			wantType:    "text",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if nm.Body != "hello world" {
					t.Errorf("body = %q", nm.Body)
				}
				if nm.ChatClass != ChatClassDM {
					t.Errorf("chatClass = %d", nm.ChatClass)
				}
				if p.Body != "hello world" {
					t.Errorf("payload body = %q", p.Body)
				}
			},
		},
		{
			name:        "text from me",
			chat:        dm,
			fromMe:      true,
			content:     &waE2E.Message{Conversation: proto.String("mine")},
			wantEvent:   domain.EventMessageFromMe,
			wantKind:    PersistMessage,
			wantSubtype: SubtypeText,
			wantType:    "text",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if !nm.FromMe || !p.FromMe {
					t.Errorf("fromMe not set")
				}
			},
		},
		{
			name:        "text in group is message not from_me",
			chat:        group,
			content:     &waE2E.Message{Conversation: proto.String("hi group")},
			wantEvent:   domain.EventMessage,
			wantKind:    PersistMessage,
			wantSubtype: SubtypeText,
			wantType:    "text",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if nm.ChatClass != ChatClassGroup {
					t.Errorf("chatClass = %d, want group", nm.ChatClass)
				}
			},
		},
		{
			name: "extended text with quote and mentions",
			chat: group,
			content: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String("reply @x"),
				ContextInfo: &waE2E.ContextInfo{
					StanzaID:     proto.String("quoted123"),
					MentionedJID: []string{"628999@s.whatsapp.net", "205227043110953:9@lid"},
				},
			}},
			wantEvent:   domain.EventMessage,
			wantKind:    PersistMessage,
			wantSubtype: SubtypeText,
			wantType:    "text",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if nm.QuotedMessageID != "quoted123" {
					t.Errorf("quoted = %q", nm.QuotedMessageID)
				}
				// Mentions are canonicalized to non-AD form (the ":9" device suffix
				// is stripped); the @lid and @s.whatsapp.net forms are both kept.
				if len(nm.Mentions) != 2 ||
					nm.Mentions[0] != "628999@s.whatsapp.net" ||
					nm.Mentions[1] != "205227043110953@lid" {
					t.Errorf("mentions = %v", nm.Mentions)
				}
				if nm.Body != "reply @x" {
					t.Errorf("body = %q", nm.Body)
				}
				if p.QuotedMessageID != "quoted123" || len(p.Mentions) != 2 {
					t.Errorf("payload quote/mentions wrong")
				}
			},
		},
		{
			name: "reaction",
			chat: dm,
			content: &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
				Text: proto.String("👍"),
				Key:  &waCommon.MessageKey{ID: proto.String("target-msg")},
			}},
			wantEvent:   domain.EventMessageReaction,
			wantKind:    PersistMessageReaction,
			wantSubtype: SubtypeReaction,
			wantType:    "reaction",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if nm.Reaction != "👍" {
					t.Errorf("reaction = %q", nm.Reaction)
				}
				if nm.TargetMessageID != "target-msg" {
					t.Errorf("target = %q", nm.TargetMessageID)
				}
				if p.Reaction != "👍" || p.TargetID != "target-msg" {
					t.Errorf("payload reaction/target wrong: %+v", p)
				}
			},
		},
		{
			name: "revoke",
			chat: dm,
			content: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
				Type: waE2E.ProtocolMessage_REVOKE.Enum(),
				Key:  &waCommon.MessageKey{ID: proto.String("deleted-msg")},
			}},
			wantEvent:   domain.EventMessageRevoked,
			wantKind:    PersistMessageRevoke,
			wantSubtype: SubtypeRevoke,
			wantType:    "revoke",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if nm.TargetMessageID != "deleted-msg" || p.TargetID != "deleted-msg" {
					t.Errorf("revoke target wrong: %q / %q", nm.TargetMessageID, p.TargetID)
				}
			},
		},
		{
			name: "edit via protocol message",
			chat: dm,
			content: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
				Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
				Key:           &waCommon.MessageKey{ID: proto.String("edited-msg")},
				EditedMessage: &waE2E.Message{Conversation: proto.String("new text")},
			}},
			wantEvent:   domain.EventMessageEdited,
			wantKind:    PersistMessageEdit,
			wantSubtype: SubtypeEdit,
			wantType:    "text",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if nm.TargetMessageID != "edited-msg" {
					t.Errorf("edit target = %q", nm.TargetMessageID)
				}
				if nm.Body != "new text" {
					t.Errorf("edit body = %q", nm.Body)
				}
			},
		},
		{
			name: "location",
			chat: dm,
			content: &waE2E.Message{LocationMessage: &waE2E.LocationMessage{
				DegreesLatitude:  proto.Float64(-8.65),
				DegreesLongitude: proto.Float64(115.21),
				Name:             proto.String("Denpasar"),
				Address:          proto.String("Bali"),
			}},
			wantEvent:   domain.EventMessage,
			wantKind:    PersistMessage,
			wantSubtype: SubtypeLocation,
			wantType:    "location",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if nm.Location == nil {
					t.Fatalf("location nil")
				}
				if nm.Location.Latitude != -8.65 || nm.Location.Longitude != 115.21 {
					t.Errorf("coords = %+v", nm.Location)
				}
				if nm.Location.Name != "Denpasar" {
					t.Errorf("loc name = %q", nm.Location.Name)
				}
				if p.Location == nil || p.Location.Name != "Denpasar" {
					t.Errorf("payload location wrong")
				}
			},
		},
		{
			name: "contact card",
			chat: dm,
			content: &waE2E.Message{ContactMessage: &waE2E.ContactMessage{
				DisplayName: proto.String("Bob"),
				Vcard:       proto.String("BEGIN:VCARD"),
			}},
			wantEvent:   domain.EventMessage,
			wantKind:    PersistMessage,
			wantSubtype: SubtypeContact,
			wantType:    "contact",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if nm.Contact == nil || nm.Contact.DisplayName != "Bob" {
					t.Errorf("contact = %+v", nm.Contact)
				}
				if nm.Contact.VCard != "BEGIN:VCARD" {
					t.Errorf("vcard = %q", nm.Contact.VCard)
				}
			},
		},
		{
			name: "poll creation",
			chat: group,
			content: &waE2E.Message{PollCreationMessage: &waE2E.PollCreationMessage{
				Name: proto.String("Lunch?"),
				Options: []*waE2E.PollCreationMessage_Option{
					{OptionName: proto.String("Pizza")},
					{OptionName: proto.String("Sushi")},
				},
				SelectableOptionsCount: proto.Uint32(1),
			}},
			wantEvent:   domain.EventMessage,
			wantKind:    PersistMessage,
			wantSubtype: SubtypePoll,
			wantType:    "poll",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if nm.Poll == nil {
					t.Fatalf("poll nil")
				}
				if nm.Poll.Name != "Lunch?" || len(nm.Poll.Options) != 2 {
					t.Errorf("poll = %+v", nm.Poll)
				}
				if nm.Poll.Options[0] != "Pizza" || nm.Poll.Options[1] != "Sushi" {
					t.Errorf("poll options = %v", nm.Poll.Options)
				}
				if nm.Poll.SelectableCount != 1 {
					t.Errorf("selectable = %d", nm.Poll.SelectableCount)
				}
			},
		},
		{
			// Modern WhatsApp clients send polls as PollCreationMessageV3 (field 64),
			// not the legacy PollCreationMessage. Both must classify as a poll.
			name: "poll creation V3",
			chat: group,
			content: &waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{
				Name: proto.String("Dinner?"),
				Options: []*waE2E.PollCreationMessage_Option{
					{OptionName: proto.String("Ramen")},
					{OptionName: proto.String("Tacos")},
				},
				SelectableOptionsCount: proto.Uint32(1),
			}},
			wantEvent:   domain.EventMessage,
			wantKind:    PersistMessage,
			wantSubtype: SubtypePoll,
			wantType:    "poll",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if nm.Poll == nil {
					t.Fatalf("poll nil")
				}
				if nm.Poll.Name != "Dinner?" || len(nm.Poll.Options) != 2 {
					t.Errorf("poll = %+v", nm.Poll)
				}
				if nm.Poll.Options[0] != "Ramen" || nm.Poll.Options[1] != "Tacos" {
					t.Errorf("poll options = %v", nm.Poll.Options)
				}
			},
		},
		{
			name: "poll vote",
			chat: group,
			content: &waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{
				PollCreationMessageKey: &waCommon.MessageKey{ID: proto.String("poll-msg-id")},
			}},
			wantEvent:   domain.EventPollVote,
			wantKind:    PersistPollVote,
			wantSubtype: SubtypePollVote,
			wantType:    "poll_vote",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if nm.PollVoteTargetID != "poll-msg-id" {
					t.Errorf("poll vote target = %q", nm.PollVoteTargetID)
				}
				if p.TargetID != "poll-msg-id" {
					t.Errorf("payload target = %q", p.TargetID)
				}
			},
		},
		{
			name: "image media metadata only",
			chat: dm,
			content: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
				Mimetype:   proto.String("image/jpeg"),
				FileLength: proto.Uint64(2048),
				Caption:    proto.String("a photo"),
			}},
			wantEvent:   domain.EventMessage,
			wantKind:    PersistMessage,
			wantSubtype: SubtypeMedia,
			wantType:    "image",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if !nm.HasMedia {
					t.Errorf("hasMedia false")
				}
				if nm.MediaInfo == nil || nm.MediaInfo.Mimetype != "image/jpeg" || nm.MediaInfo.Size != 2048 {
					t.Errorf("mediaInfo = %+v", nm.MediaInfo)
				}
				if nm.Body != "a photo" {
					t.Errorf("caption = %q", nm.Body)
				}
				// §9: media is ALWAYS null on the wire even when present.
				if p.Media != nil {
					t.Errorf("payload media must be null, got %+v", p.Media)
				}
				if !p.HasMedia {
					t.Errorf("payload hasMedia false")
				}
			},
		},
		{
			name: "document media filename",
			chat: dm,
			content: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
				Mimetype:   proto.String("application/pdf"),
				FileLength: proto.Uint64(1000),
				FileName:   proto.String("report.pdf"),
			}},
			wantEvent:   domain.EventMessage,
			wantKind:    PersistMessage,
			wantSubtype: SubtypeMedia,
			wantType:    "document",
			check: func(t *testing.T, nm *NormalizedMessage, p MessagePayload) {
				if nm.MediaInfo == nil || nm.MediaInfo.Filename != "report.pdf" {
					t.Errorf("doc mediaInfo = %+v", nm.MediaInfo)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := msgEvent(tt.chat, "628222@s.whatsapp.net", tt.fromMe, tt.content)
			ev, pr, ok := Normalize(e, testSession, testOrganization)
			if !ok {
				t.Fatalf("Normalize returned ok=false")
			}
			if ev.Type != tt.wantEvent {
				t.Errorf("event type = %q, want %q", ev.Type, tt.wantEvent)
			}
			if ev.Session != testSession || ev.Organization != testOrganization {
				t.Errorf("session/organization not propagated: %q/%q", ev.Session, ev.Organization)
			}
			if ev.Schema != domain.Schema {
				t.Errorf("schema = %q", ev.Schema)
			}
			if pr.Kind != tt.wantKind {
				t.Errorf("persist kind = %d, want %d", pr.Kind, tt.wantKind)
			}
			if pr.Message == nil {
				t.Fatalf("PersistResult.Message nil")
			}
			nm := pr.Message
			if nm.Subtype != tt.wantSubtype {
				t.Errorf("subtype = %d, want %d", nm.Subtype, tt.wantSubtype)
			}
			if nm.MessageType != tt.wantType {
				t.Errorf("messageType = %q, want %q", nm.MessageType, tt.wantType)
			}
			if nm.WAMessageID != "wamid.TEST" {
				t.Errorf("waMessageID = %q", nm.WAMessageID)
			}
			if nm.Timestamp != 1719400000000 {
				t.Errorf("timestamp = %d", nm.Timestamp)
			}
			p, isMsg := ev.Payload.(MessagePayload)
			if !isMsg {
				t.Fatalf("payload not MessagePayload: %T", ev.Payload)
			}
			if tt.check != nil {
				tt.check(t, nm, p)
			}
		})
	}
}

func TestNormalizeMessageSenderLID(t *testing.T) {
	e := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:      mustJID(t, "628111@s.whatsapp.net"),
				Sender:    mustJID(t, "628222@s.whatsapp.net"),
				SenderAlt: mustJID(t, "777@lid"),
			},
			ID:        "wamid.LID",
			Timestamp: time.UnixMilli(1),
		},
		Message: &waE2E.Message{Conversation: proto.String("hi")},
	}
	_, pr, ok := Normalize(e, testSession, testOrganization)
	if !ok || pr.Message == nil {
		t.Fatalf("normalize failed")
	}
	if pr.Message.SenderLID != "777@lid" {
		t.Errorf("senderLID = %q, want 777@lid", pr.Message.SenderLID)
	}
	if pr.Message.SenderJID != "628222@s.whatsapp.net" {
		t.Errorf("senderJID = %q", pr.Message.SenderJID)
	}
}

func TestNormalizeMessageSenderAltPN_NotTreatedAsLID(t *testing.T) {
	// When SenderAlt is a phone-number form (not on the lid server), SenderLID
	// must stay empty.
	e := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:      mustJID(t, "628111@s.whatsapp.net"),
				Sender:    mustJID(t, "777@lid"),
				SenderAlt: mustJID(t, "628222@s.whatsapp.net"),
			},
			ID:        "wamid.PN",
			Timestamp: time.UnixMilli(1),
		},
		Message: &waE2E.Message{Conversation: proto.String("hi")},
	}
	_, pr, _ := Normalize(e, testSession, testOrganization)
	if pr.Message.SenderLID != "" {
		t.Errorf("senderLID = %q, want empty (alt is PN)", pr.Message.SenderLID)
	}
}
