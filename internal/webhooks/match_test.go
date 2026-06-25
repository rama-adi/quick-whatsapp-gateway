package webhooks

import "testing"

func TestEventMatches(t *testing.T) {
	tests := []struct {
		name      string
		events    []string
		eventType string
		want      bool
	}{
		{"wildcard matches all", []string{"*"}, "message", true},
		{"wildcard matches unusual type", []string{"*"}, "poll.vote", true},
		{"exact subset match", []string{"message", "poll.vote"}, "poll.vote", true},
		{"first element match", []string{"message", "poll.vote"}, "message", true},
		{"no match in subset", []string{"message", "poll.vote"}, "session.status", false},
		{"empty list matches nothing", nil, "message", false},
		{"empty list with wildcard target", []string{}, "*", false},
		{"wildcard mixed with others still matches", []string{"message", "*"}, "anything", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EventMatches(tt.events, tt.eventType); got != tt.want {
				t.Fatalf("EventMatches(%v, %q) = %v, want %v", tt.events, tt.eventType, got, tt.want)
			}
		})
	}
}
