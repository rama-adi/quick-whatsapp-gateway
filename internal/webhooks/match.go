package webhooks

// EventMatches reports whether a webhook's events list subscribes to eventType.
// A list containing "*" matches every event; otherwise an exact element match is
// required. An empty list matches nothing (a webhook with no subscriptions is
// effectively inert). This is the same rule the repo applies in SQL; the
// dispatcher uses it as a defensive in-memory guard.
func EventMatches(events []string, eventType string) bool {
	for _, e := range events {
		if e == "*" || e == eventType {
			return true
		}
	}
	return false
}
