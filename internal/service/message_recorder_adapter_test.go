package service

import (
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func TestChatTypeFromJID(t *testing.T) {
	cases := map[string]domain.ChatType{
		"628123@s.whatsapp.net": domain.ChatDM,
		"55667788@lid":          domain.ChatDM,
		"120363@g.us":           domain.ChatGroup,
		"0@newsletter":          domain.ChatNewsletter,
		"123@broadcast":         domain.ChatBroadcast,
		"status@broadcast":      domain.ChatBroadcast,
		"":                      domain.ChatDM,
	}
	for jid, want := range cases {
		if got := chatTypeFromJID(jid); got != want {
			t.Errorf("chatTypeFromJID(%q) = %q, want %q", jid, got, want)
		}
	}
}
