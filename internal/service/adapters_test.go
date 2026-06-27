package service

import (
	"encoding/json"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	waevents "github.com/ramaadi/quick-whatsapp-gateway/internal/wa/events"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/inbound"
)

func TestInboundMessageFromEventsMessage_LIDSenderAndGroupAccounting(t *testing.T) {
	payload := waevents.MessagePayload{
		WAMessageID:     "MSG_SYNTHETIC_GROUP_TEXT",
		ChatJID:         "group-test@g.us",
		SenderJID:       "sender-test@lid",
		FromMe:          false,
		Type:            "text",
		Body:            "synthetic group text",
		QuotedMessageID: "MSG_SYNTHETIC_QUOTED",
		HasMedia:        false,
		Timestamp:       1782554804000,
		PushName:        "Synthetic Sender",
	}
	ev := domain.NewEvent(domain.EventMessage, "sess_1", "org_1", payload)

	nm := inboundMessageFromEventsMessage(&waevents.NormalizedMessage{
		WAMessageID:     payload.WAMessageID,
		ChatJID:         payload.ChatJID,
		ChatClass:       waevents.ChatClassGroup,
		SenderJID:       payload.SenderJID,
		FromMe:          payload.FromMe,
		PushName:        payload.PushName,
		Timestamp:       payload.Timestamp,
		Subtype:         waevents.SubtypeText,
		MessageType:     payload.Type,
		Body:            payload.Body,
		QuotedMessageID: payload.QuotedMessageID,
	}, inbound.KindMessage, ev, "sess_1", "org_1")

	if nm.SenderLID != "sender-test@lid" {
		t.Fatalf("SenderLID = %q", nm.SenderLID)
	}
	if nm.SenderJID != "" {
		t.Fatalf("SenderJID = %q, want empty when sender is LID-only", nm.SenderJID)
	}
	if !nm.IsGroup || nm.Group == nil || nm.Group.GroupJID != payload.ChatJID {
		t.Fatalf("group capture missing: isGroup=%v group=%+v", nm.IsGroup, nm.Group)
	}
	if len(nm.Members) != 1 || nm.Members[0].LID != nm.SenderLID || nm.Members[0].Nickname != "Synthetic Sender" {
		t.Fatalf("members = %+v", nm.Members)
	}
	if nm.Body != "synthetic group text" || nm.QuotedMessageID != "MSG_SYNTHETIC_QUOTED" {
		t.Fatalf("message fields body=%q quoted=%q", nm.Body, nm.QuotedMessageID)
	}
	var raw waevents.MessagePayload
	if err := json.Unmarshal(nm.RawJSON, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.SenderJID != payload.SenderJID || raw.Body != payload.Body {
		t.Fatalf("raw payload = %+v", raw)
	}
}

func TestSplitSenderIDs_PhoneAndLID(t *testing.T) {
	lid, phoneJID := splitSenderIDs("777@lid", "628222@s.whatsapp.net")
	if lid != "777@lid" || phoneJID != "628222@s.whatsapp.net" {
		t.Fatalf("split alt lid = %q %q", lid, phoneJID)
	}

	lid, phoneJID = splitSenderIDs("", "sender-test@lid")
	if lid != "sender-test@lid" || phoneJID != "" {
		t.Fatalf("split primary lid = %q %q", lid, phoneJID)
	}
}
