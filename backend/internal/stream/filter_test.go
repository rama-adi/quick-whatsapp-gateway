package stream

import "testing"

// TestParseEventFilter runs empty, wildcard, comma-separated, whitespace, duplicate, and invalid event
// selections through the filter parser. Empty and wildcard mean all events, explicit names form a
// deduplicated allow-list, and a list containing no valid names matches nothing. This defines the
// subscription query semantics before Redis is touched.
func TestParseEventFilter(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantAll   bool
		wantEmpty bool
		allows    map[string]bool // type -> expected allows()
	}{
		{
			name:    "empty means all",
			raw:     "",
			wantAll: true,
			allows:  map[string]bool{"message": true, "poll.vote": true},
		},
		{
			name:    "star means all",
			raw:     "*",
			wantAll: true,
			allows:  map[string]bool{"anything": true},
		},
		{
			name:    "star within list means all",
			raw:     "message,*,poll.vote",
			wantAll: true,
			allows:  map[string]bool{"session.status": true},
		},
		{
			name:   "single type",
			raw:    "message",
			allows: map[string]bool{"message": true, "poll.vote": false},
		},
		{
			name:   "comma list with spaces",
			raw:    " message , poll.vote ",
			allows: map[string]bool{"message": true, "poll.vote": true, "chat.update": false},
		},
		{
			name:   "blank tokens ignored",
			raw:    "message,,,poll.vote",
			allows: map[string]bool{"message": true, "poll.vote": true},
		},
		{
			name:      "only blanks is empty",
			raw:       " , , ",
			wantEmpty: true,
			allows:    map[string]bool{"message": false},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := parseEventFilter(tc.raw)
			if f.all != tc.wantAll {
				t.Errorf("all = %v, want %v", f.all, tc.wantAll)
			}
			if f.empty() != tc.wantEmpty {
				t.Errorf("empty() = %v, want %v", f.empty(), tc.wantEmpty)
			}
			for typ, want := range tc.allows {
				if got := f.allows(typ); got != want {
					t.Errorf("allows(%q) = %v, want %v", typ, got, want)
				}
			}
		})
	}
}

// TestChannelNaming checks exact session channels, organization patterns, firehose patterns, and an
// organization id containing Redis glob metacharacters. The organization pattern must escape *, ?,
// brackets, and backslashes while leaving the final session wildcard active. This prevents crafted tenant
// ids from subscribing to another organizations events.
func TestChannelNaming(t *testing.T) {
	if got := sessionChannel("ten_a", "sess_1"); got != "evt:ten_a:sess_1" {
		t.Errorf("sessionChannel = %q", got)
	}
	if got := organizationPattern("ten_a"); got != "evt:ten_a:*" {
		t.Errorf("organizationPattern = %q", got)
	}
	if got := organizationPattern(`ten*[a]?\b`); got != `evt:ten\*\[a\]\?\\b:*` {
		t.Errorf("organizationPattern did not escape glob metacharacters: %q", got)
	}
	// A organization pattern must not match another organization's channel.
	if got := channelFor("ten_b", "sess_1"); got == "evt:ten_a:sess_1" {
		t.Errorf("channelFor leaked across organizations: %q", got)
	}
}
