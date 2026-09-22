package outbound

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestInteractiveBuildAndDispatch(t *testing.T) {
	requests := []domain.SendRequest{
		{Type: domain.SendTypeButtons, To: "123@g.us", Text: "Choose", Buttons: []domain.ReplyButton{{ID: "sku\"1", Title: "One"}}},
		{Type: domain.SendTypeList, To: "123@g.us", Text: "Choose", List: &domain.SelectionList{Title: "Open", Sections: []domain.ListSection{{Title: "Products", Rows: []domain.ListRow{{ID: "sku1", Title: "One", Description: "First"}}}}}},
	}
	for _, req := range requests {
		t.Run(req.Type, func(t *testing.T) {
			if err := Validate(req); err != nil {
				t.Fatal(err)
			}
			msg, err := buildInteractive(req, &waE2E.ContextInfo{StanzaID: proto.String("original")})
			if err != nil {
				t.Fatal(err)
			}
			interactive := msg.GetInteractiveMessage()
			if interactive.GetBody().GetText() != "Choose" || interactive.GetContextInfo().GetStanzaID() != "original" {
				t.Fatal("lost text or quote")
			}
			button := interactive.GetNativeFlowMessage().GetButtons()[0]
			if req.Type == domain.SendTypeButtons {
				var params map[string]string
				if err := json.Unmarshal([]byte(button.GetButtonParamsJSON()), &params); err != nil {
					t.Fatal(err)
				}
				if button.GetName() != "quick_reply" || params["id"] != "sku\"1" || params["display_text"] != "One" {
					t.Fatalf("bad quick reply: %v", params)
				}
			} else {
				var menu domain.SelectionList
				if err := json.Unmarshal([]byte(button.GetButtonParamsJSON()), &menu); err != nil {
					t.Fatal(err)
				}
				if button.GetName() != "single_select" || menu.Sections[0].Rows[0].ID != "sku1" {
					t.Fatalf("bad list: %+v", menu)
				}
			}
			wa := newFakeWA()
			sender := &Sender{wa: wa}
			if _, _, err := sender.Dispatch(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if len(wa.calls) != 1 || wa.calls[0] != "SendInteractive" || wa.lastTo != req.To {
				t.Fatal("interactive dispatch lost group destination")
			}
			if outboundBody(req) != req.Text {
				t.Fatal("interactive text lost from history")
			}
		})
	}
}

func TestInteractiveValidation(t *testing.T) {
	cases := []domain.SendRequest{
		{Type: "buttons", To: "123@g.us", Text: "Choose"},
		{Type: "buttons", To: "123@g.us", Buttons: []domain.ReplyButton{{ID: "a", Title: "A"}}},
		{Type: "buttons", To: "123@g.us", Text: "Choose", Buttons: []domain.ReplyButton{{ID: "a", Title: "A"}, {ID: "a", Title: "B"}}},
		{Type: "buttons", To: "123@g.us", Text: "Choose", Buttons: []domain.ReplyButton{{ID: " ", Title: "A"}}},
		{Type: "list", To: "123@g.us", Text: "Choose"},
		{Type: "list", To: "123@g.us", Text: "Choose", List: &domain.SelectionList{Title: "Open", Sections: []domain.ListSection{{Title: "Empty"}}}},
		{Type: "list", To: "123@g.us", Text: "Choose", List: &domain.SelectionList{Title: "Open", Sections: []domain.ListSection{{Rows: []domain.ListRow{{ID: "a", Title: "A"}}}, {Rows: []domain.ListRow{{ID: "a", Title: "B"}}}}}},
	}
	for _, req := range cases {
		if err := Validate(req); err == nil {
			t.Errorf("accepted invalid request: %+v", req)
		}
	}
}
