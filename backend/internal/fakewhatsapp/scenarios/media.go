package scenarios

import "github.com/rama-adi/quick-whatsapp-gateway/internal/fakewhatsapp/scenario"

var Media = scenario.Define(scenario.Story{
	ID: "media", Name: "Media exchange",
	Description: "An image and a document with bytes stored by the fake peer.",
	Steps: []scenario.Step{
		scenario.Healthy(),
		Alex.SendsImage("A tiny image", "image/png", tinyPNG()),
		Alex.SendsDocument("Meeting notes", "notes.txt", "text/plain", meetingNotes()),
	},
})
