package main

import (
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
)

func runE2ESendTypes(t *testing.T, infra *e2eInfra, gateway *e2eGateway) {
	media := base64.StdEncoding.EncodeToString([]byte("isolated media bytes"))
	headerImage := "iVBORw0KGgoAAAANSUhEUgAAACAAAAAgCAIAAAD8GO2jAAAAKklEQVR4nGOQL99NU8QwasGoBaMWjFowasGoBaMWjFowasGoBaMWDBULALrlREz4/hvCAAAAAElFTkSuQmCC"
	cases := []struct {
		name     string
		request  domain.SendRequest
		captures int
		check    func(*waE2E.Message) bool
	}{
		{name: "text", request: domain.SendRequest{Type: domain.SendTypeText, Text: "plain text"},
			captures: 1, check: func(m *waE2E.Message) bool { return m.GetConversation() == "plain text" }},
		{name: "image", request: domain.SendRequest{Type: domain.SendTypeImage,
			Media: &domain.MediaPayload{Data: media, Mimetype: "image/png", Caption: "image caption"}},
			captures: 1, check: func(m *waE2E.Message) bool { return m.GetImageMessage().GetCaption() == "image caption" }},
		{name: "video", request: domain.SendRequest{Type: domain.SendTypeVideo,
			Media: &domain.MediaPayload{Data: media, Mimetype: "video/mp4", Caption: "video caption"}},
			captures: 1, check: func(m *waE2E.Message) bool { return m.GetVideoMessage().GetCaption() == "video caption" }},
		{name: "audio", request: domain.SendRequest{Type: domain.SendTypeAudio,
			Media: &domain.MediaPayload{Data: media, Mimetype: "audio/ogg"}},
			captures: 1, check: func(m *waE2E.Message) bool { return m.GetAudioMessage() != nil }},
		{name: "document", request: domain.SendRequest{Type: domain.SendTypeDocument,
			Media: &domain.MediaPayload{Data: media, Mimetype: "application/pdf", Filename: "test.pdf", Caption: "document"}},
			captures: 1, check: func(m *waE2E.Message) bool { return m.GetDocumentMessage().GetFileName() == "test.pdf" }},
		{name: "sticker", request: domain.SendRequest{Type: domain.SendTypeSticker,
			Media: &domain.MediaPayload{Data: media, Mimetype: "image/webp"}},
			captures: 1, check: func(m *waE2E.Message) bool { return m.GetStickerMessage() != nil }},
		{name: "poll", request: domain.SendRequest{Type: domain.SendTypePoll,
			Name: "Lunch?", Options: []string{"A", "B"}, SelectableCount: 1},
			captures: 1, check: func(m *waE2E.Message) bool {
				return m.GetPollCreationMessage() != nil || m.GetPollCreationMessageV3() != nil
			}},
		{name: "location", request: domain.SendRequest{Type: domain.SendTypeLocation,
			Name: "Office", Latitude: -8.65, Longitude: 115.21},
			captures: 1, check: func(m *waE2E.Message) bool { return m.GetLocationMessage() != nil }},
		{name: "contact", request: domain.SendRequest{Type: domain.SendTypeContact,
			Contact: &domain.ContactCard{Name: "Alice", Phone: "628123456789"}},
			captures: 1, check: func(m *waE2E.Message) bool { return m.GetContactMessage() != nil }},
		{name: "album", request: domain.SendRequest{Type: domain.SendTypeAlbum, Caption: "Trip",
			Medias: []domain.AlbumMediaPayload{
				{Type: domain.SendTypeImage, Data: media, Mimetype: "image/png"},
				{Type: domain.SendTypeVideo, Data: media, Mimetype: "video/mp4"},
			}},
			captures: 3, check: func(m *waE2E.Message) bool { return m.GetAlbumMessage() != nil }},
		{name: "buttons", request: domain.SendRequest{Type: domain.SendTypeButtons,
			Text: "Choose", Footer: "Only caller replies", Buttons: []domain.ReplyButton{
				{ID: "one", Title: "12345678901234567890"},
				{ID: "two", Title: "Two"},
				{ID: "three", Title: "Three"},
			}},
			captures: 1, check: func(m *waE2E.Message) bool {
				flow := m.GetInteractiveMessage().GetNativeFlowMessage()
				return flow != nil && len(flow.GetButtons()) == 3 && flow.GetButtons()[0].GetButtonParamsJSON() != "" &&
					m.GetInteractiveMessage().GetBody().GetText() == "Choose\n"
			}},
		{name: "buttons-url", request: domain.SendRequest{Type: domain.SendTypeButtons,
			Text: "Open URL", Buttons: []domain.ReplyButton{{Kind: "url", Title: "Visit", URL: "https://example.com/test"}}},
			captures: 1, check: func(m *waE2E.Message) bool {
				buttons := m.GetInteractiveMessage().GetNativeFlowMessage().GetButtons()
				return len(buttons) == 1 && buttons[0].GetName() == "cta_url" &&
					buttons[0].GetButtonParamsJSON() == `{"display_text":"Visit","url":"https://example.com/test","merchant_url":"https://example.com/test"}`
			}},
		{name: "buttons-copy", request: domain.SendRequest{Type: domain.SendTypeButtons,
			Text: "Copy code", Buttons: []domain.ReplyButton{{Kind: "copy", Title: "Copy", Code: "TEST-123"}}},
			captures: 1, check: func(m *waE2E.Message) bool {
				buttons := m.GetInteractiveMessage().GetNativeFlowMessage().GetButtons()
				return len(buttons) == 1 && buttons[0].GetName() == "cta_copy" &&
					buttons[0].GetButtonParamsJSON() == `{"display_text":"Copy","copy_code":"TEST-123"}`
			}},
		{name: "buttons-header", request: domain.SendRequest{Type: domain.SendTypeButtons,
			Text: "Image header", HeaderImage: &domain.MediaPayload{Data: headerImage, Mimetype: "image/png"},
			Buttons: []domain.ReplyButton{{ID: "seen", Title: "Seen"}}},
			captures: 1, check: func(m *waE2E.Message) bool {
				interactive := m.GetInteractiveMessage()
				header := interactive.GetHeader()
				return header.GetHasMediaAttachment() && header.GetImageMessage().GetURL() != "" &&
					header.GetImageMessage().GetWidth() == 32 &&
					interactive.GetNativeFlowMessage().GetButtons()[0].GetName() == "quick_reply"
			}},
		{name: "list", request: domain.SendRequest{Type: domain.SendTypeList, Text: "Pick",
			List: &domain.SelectionList{Title: "Choices", Sections: []domain.ListSection{{
				Title: "Section", Rows: []domain.ListRow{{ID: "item", Title: "Item"}},
			}}}},
			captures: 1, check: func(m *waE2E.Message) bool {
				list := m.GetListMessage()
				return list != nil && list.GetListType() == waE2E.ListMessage_SINGLE_SELECT &&
					list.GetDescription() == "Pick" && list.GetButtonText() == "Choices" &&
					len(list.GetSections()) == 1 && len(list.GetSections()[0].GetRows()) == 1 &&
					list.GetSections()[0].GetRows()[0].GetRowID() == "item"
			}},
	}
	for _, tc := range cases {
		t.Run("send type "+tc.name, func(t *testing.T) {
			before := len(gateway.getCaptures(t))
			tc.request.To = e2eGroupJID
			status, result := infra.send(t, e2eOrgAKey, "send-type-"+tc.name, tc.request)
			if status != http.StatusOK || result.Mode != outbound.ModeSync || result.WAMessageID == "" {
				t.Fatalf("send %s = status %d result %+v", tc.name, status, result)
			}
			captures := gateway.waitCaptureCount(t, before+tc.captures)
			primary := captures[before]
			if primary.ID != result.WAMessageID || primary.To != e2eGroupJID {
				t.Fatalf("capture/result mismatch: %+v %+v", primary, result)
			}
			message := &waE2E.Message{}
			if err := protojson.Unmarshal(primary.Message, message); err != nil {
				t.Fatal(err)
			}
			if !tc.check(message) {
				t.Fatalf("send %s emitted wrong WhatsApp payload: %+v", tc.name, message)
			}
			readStatus, messages := infra.messages(t, e2eOrgAKey, e2eGroupJID)
			if readStatus != http.StatusOK {
				t.Fatalf("history status = %d", readStatus)
			}
			found := false
			for _, record := range messages {
				if record.WAMessageID == result.WAMessageID && record.FromMe && record.Type == tc.request.Type {
					found = true
				}
			}
			if !found {
				t.Fatalf("send %s missing from history", tc.name)
			}
		})
	}
}
