package scenarios

import "github.com/rama-adi/quick-whatsapp-gateway/internal/fakewhatsapp/scenario"

var Alex = scenario.Person("555000002", "Alex Example")
var Bea = scenario.Person("555000003", "Bea Example")
var Team = scenario.GroupWithUsers("team", "Fake team", Alex, Bea)
