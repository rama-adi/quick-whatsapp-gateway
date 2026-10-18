package scenarios

import "github.com/rama-adi/quick-whatsapp-gateway/internal/fakewhatsapp/scenario"

var LargeHistory = scenario.Define(scenario.Story{
	ID:          "large-history",
	Name:        "Large history sync",
	Description: "The phone sends its chat history to the linked gateway: the existing chats plus 50 chats with 400 messages each (20,000 messages).",
	Steps:       []scenario.Step{scenario.Healthy(), scenario.SyncHistory(50, 400)},
})

var ExpiredHistory = scenario.Define(scenario.Story{
	ID:          "expired-history",
	Name:        "Expired history blob",
	Description: "The phone sends a history sync whose downloadable chunks were already deleted from WhatsApp's servers (HTTP 404). The gateway should ask the phone to upload them again.",
	Steps:       []scenario.Step{scenario.Healthy(), scenario.ExpiredHistory()},
})
