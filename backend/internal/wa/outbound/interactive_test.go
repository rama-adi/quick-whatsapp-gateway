package outbound

import (
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

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
