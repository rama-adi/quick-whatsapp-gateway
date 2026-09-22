package events

import "testing"

// TestShouldIgnore runs the filter across status, group, newsletter, broadcast, direct, LID, and malformed
// chat identifiers under both enabled and disabled flags. The matrix pins flag independence and the
// fail-open rule for unknown addresses so user traffic is never discarded by accident.
func TestShouldIgnore(t *testing.T) {
	all := IgnoreConfig{IgnoreStatus: true, IgnoreGroups: true, IgnoreChannels: true, IgnoreBroadcast: true}
	none := IgnoreConfig{}

	tests := []struct {
		name    string
		cfg     IgnoreConfig
		chatJID string
		want    bool
	}{
		{"status ignored when on", all, "status@broadcast", true},
		{"status kept when off", none, "status@broadcast", false},
		{"status independent of broadcast flag", IgnoreConfig{IgnoreBroadcast: true}, "status@broadcast", false},
		{"group ignored when on", all, "12036@g.us", true},
		{"group kept when off", none, "12036@g.us", false},
		{"newsletter ignored when on", all, "12345@newsletter", true},
		{"newsletter kept when off", none, "12345@newsletter", false},
		{"broadcast list ignored when on", all, "9999@broadcast", true},
		{"broadcast kept when off", none, "9999@broadcast", false},
		{"broadcast independent of status flag", IgnoreConfig{IgnoreStatus: true}, "9999@broadcast", false},
		{"dm never ignored", all, "628123@s.whatsapp.net", false},
		{"lid dm never ignored", all, "55555@lid", false},
		{"unparseable fails open", all, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewIgnoreRules(tt.cfg)
			if got := r.ShouldIgnore(tt.chatJID); got != tt.want {
				t.Fatalf("ShouldIgnore(%q) = %v, want %v", tt.chatJID, got, tt.want)
			}
		})
	}
}

// TestClassifyChat maps phone, LID, group, newsletter, broadcast, and status JIDs to their chat classes.
// It fixes the server-suffix boundary used by ignore rules and downstream routing.
func TestClassifyChat(t *testing.T) {
	tests := []struct {
		jid  string
		want ChatClass
	}{
		{"628123@s.whatsapp.net", ChatClassDM},
		{"55555@lid", ChatClassDM},
		{"12036@g.us", ChatClassGroup},
		{"12345@newsletter", ChatClassNewsletter},
		{"9999@broadcast", ChatClassBroadcast},
		{"status@broadcast", ChatClassStatus},
	}
	for _, tt := range tests {
		t.Run(tt.jid, func(t *testing.T) {
			jid := mustJID(t, tt.jid)
			if got := ClassifyChat(jid); got != tt.want {
				t.Fatalf("ClassifyChat(%q) = %d, want %d", tt.jid, got, tt.want)
			}
		})
	}
}
