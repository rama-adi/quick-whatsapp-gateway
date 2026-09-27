package scenarios

import "github.com/rama-adi/quick-whatsapp-gateway/internal/fakewhatsapp/scenario"

var Conversation = scenario.Define(scenario.Story{
	ID:          "conversation",
	Name:        "A busy conversation",
	Description: "Text, read receipt, location, contact, poll, and a new group conversation.",
	Steps: []scenario.Step{
		scenario.Healthy(),
		Alex.Says("Hello from the fake phone"),
		Alex.Replies(scenario.LastMessage, "I can make it"),
		scenario.Read(Alex.JID(), scenario.LastMessage),
		Alex.SharesLocation("Meeting point", -8.6525, 115.2191),
		Alex.SharesContact("Bea Example", Bea.Phone),
		Alex.Asks("When should we meet?", "Morning", "Afternoon"),
		Team,
		Alex.SaysIn(Team.Ref(), "Welcome to the fake team"),
	},
})
