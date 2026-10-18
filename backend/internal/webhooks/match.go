package webhooks

import "github.com/rama-adi/quick-whatsapp-gateway/internal/domain"

// EventMatches reports whether a webhook's events list subscribes to eventType.
// A list containing "*" matches every event except bulk events
// (domain.IsBulkEvent, e.g. history.batch), which need an exact element match;
// otherwise an exact element match is required. An empty list matches nothing
// (a webhook with no subscriptions is effectively inert). The enqueuer's repo
// adapter and the enqueuer itself both apply this rule.
func EventMatches(events []string, eventType string) bool {
	bulk := domain.IsBulkEvent(eventType)
	for _, e := range events {
		if e == eventType || (e == "*" && !bulk) {
			return true
		}
	}
	return false
}
