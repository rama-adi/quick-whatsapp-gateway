package events

import (
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func nativeReply(name, params string) *waE2E.Message {
	return &waE2E.Message{InteractiveResponseMessage: &waE2E.InteractiveResponseMessage{
		ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("original")},
		InteractiveResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage_{
			NativeFlowResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage{
				Name: proto.String(name), ParamsJSON: proto.String(params),
			},
		},
	}}
}

func TestInteractiveReplyNormalization(t *testing.T) {
	cases := []struct {
		name string
		msg  *waE2E.Message
		kind string
	}{
		{name: "native button", msg: nativeReply("quick_reply", `{"id":"sku1","display_text":"One"}`), kind: "button"},
		{name: "native list", msg: nativeReply("single_select", `{"id":"sku1","title":"One"}`), kind: "list"},
		{name: "legacy button", msg: &waE2E.Message{ButtonsResponseMessage: &waE2E.ButtonsResponseMessage{
			SelectedButtonID: proto.String("sku1"), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("original")},
		}}, kind: "button"},
		{name: "legacy list", msg: &waE2E.Message{ListResponseMessage: &waE2E.ListResponseMessage{
			SingleSelectReply: &waE2E.ListResponseMessage_SingleSelectReply{SelectedRowID: proto.String("sku1")},
			ContextInfo:       &waE2E.ContextInfo{StanzaID: proto.String("original")},
		}}, kind: "list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, pr, ok := normalizeMessage(msgEvent("123@g.us", "456@s.whatsapp.net", false, tc.msg), testSession, testOrganization)
			if !ok || ev.Type != domain.EventMessageInteractiveReply || pr.Kind != PersistMessage {
				t.Fatalf("wrong event: %+v", ev)
			}
			p := ev.Payload.(MessagePayload)
			if p.InteractiveReply == nil || p.InteractiveReply.ID != "sku1" || p.InteractiveReply.Kind != tc.kind {
				t.Fatalf("lost selection: %+v", p)
			}
			if p.ChatJID != "123@g.us" || p.SenderJID != "456@s.whatsapp.net" || p.QuotedMessageID != "original" {
				t.Fatalf("lost correlation: %+v", p)
			}
		})
	}
}

func TestMalformedInteractiveReplyDoesNotEmitSelection(t *testing.T) {
	for _, msg := range []*waE2E.Message{
		nativeReply("quick_reply", `{`), nativeReply("single_select", `{}`),
		nativeReply("quick_reply", `{"id":42}`), nativeReply("flow", `{"id":"sku1"}`),
	} {
		nm := &NormalizedMessage{}
		classify(msgEvent("123@g.us", "456@s.whatsapp.net", false, msg), msg, nm)
		if nm.InteractiveReply != nil || nm.Subtype == SubtypeInteractiveReply {
			t.Fatal("malformed/unsupported reply emitted a selection")
		}
	}
}
