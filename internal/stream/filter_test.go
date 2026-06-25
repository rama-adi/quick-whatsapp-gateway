package stream

import "testing"

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

func TestChannelNaming(t *testing.T) {
	if got := sessionChannel("ten_a", "sess_1"); got != "evt:ten_a:sess_1" {
		t.Errorf("sessionChannel = %q", got)
	}
	if got := tenantPattern("ten_a"); got != "evt:ten_a:*" {
		t.Errorf("tenantPattern = %q", got)
	}
	// A tenant pattern must not match another tenant's channel.
	if got := channelFor("ten_b", "sess_1"); got == "evt:ten_a:sess_1" {
		t.Errorf("channelFor leaked across tenants: %q", got)
	}
}
