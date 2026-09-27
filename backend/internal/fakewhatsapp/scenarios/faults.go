package scenarios

import "github.com/rama-adi/quick-whatsapp-gateway/internal/fakewhatsapp/scenario"

var SendFailure = scenario.Define(scenario.Story{
	ID: "send-failure", Name: "Send fails before commit",
	Description: "Reject outgoing messages before the fake peer records a commit.",
	Steps:       []scenario.Step{scenario.RejectSends()},
})
var AckGap = scenario.Define(scenario.Story{
	ID: "ack-gap", Name: "Commit with lost acknowledgement",
	Description: "Record outgoing messages, then report a lost acknowledgement to the gateway.",
	Steps:       []scenario.Step{scenario.LoseAcknowledgement()},
})
var BlockedSend = scenario.Define(scenario.Story{
	ID: "blocked-send", Name: "Pause an outgoing send",
	Description: "Hold the next outgoing send at a controllable barrier until released.",
	Steps:       []scenario.Step{scenario.PauseSends()},
})
var Disconnect = scenario.Define(scenario.Story{
	ID: "disconnect", Name: "Drop the connection",
	Description: "Disconnect active fake devices and reject reconnects until behavior changes.",
	Steps:       []scenario.Step{scenario.FailConnection()},
})
