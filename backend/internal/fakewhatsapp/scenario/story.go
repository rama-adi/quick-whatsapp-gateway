package scenario

// Story is a named, ordered set of fake peer actions.
type Story struct {
	ID          string
	Name        string
	Description string
	Steps       []Step
}

// Step is closed to the typed actions in this package.
type Step interface{ isStep() }

// Repeat expands a caller-chosen number of actions in deterministic order.
func Repeat(count int, steps ...Step) []Step {
	if count < 0 {
		panic("fake WhatsApp repeat count must be nonnegative")
	}
	result := []Step{}
	for i := 0; i < count; i++ {
		result = append(result, steps...)
	}
	return result
}
