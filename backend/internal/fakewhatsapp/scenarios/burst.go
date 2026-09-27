package scenarios

import "github.com/rama-adi/quick-whatsapp-gateway/internal/fakewhatsapp/scenario"

var Burst = scenario.Define(scenario.Story{
	ID: "quick-burst", Name: "Quick message burst",
	Description: "Repeated incoming messages from the same named contact for spot checks.",
	Steps:       append([]scenario.Step{scenario.Healthy()}, scenario.Repeat(3, Bea.Says("Ping from Bea"))...),
})
