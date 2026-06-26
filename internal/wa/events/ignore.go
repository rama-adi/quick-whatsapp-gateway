package events

import "go.mau.fi/whatsmeow/types"

// IgnoreConfig holds the four source-level filtering flags from §12
// (IGNORE_STATUS / IGNORE_GROUPS / IGNORE_CHANNELS / IGNORE_BROADCAST).
//
// It is a small consumer-defined struct intentionally NOT imported from
// internal/config: this package depends only on the booleans it needs, and the
// composition root wires the real config in. Each flag, when true, makes the inbound
// pipeline skip persistence + fan-out for that source class (§7).
type IgnoreConfig struct {
	IgnoreStatus    bool // status@broadcast (the "stories" feed)
	IgnoreGroups    bool // g.us group chats
	IgnoreChannels  bool // newsletter (WhatsApp Channels)
	IgnoreBroadcast bool // broadcast lists (server == "broadcast", excluding the status JID)
}

// IgnoreRules classifies a chat JID against the configured ignore flags. It is
// constructed once with the config and reused per event — it holds no mutable
// state, so it is safe for concurrent use.
type IgnoreRules struct {
	cfg IgnoreConfig
}

// NewIgnoreRules builds an IgnoreRules from the four config flags.
func NewIgnoreRules(cfg IgnoreConfig) *IgnoreRules {
	return &IgnoreRules{cfg: cfg}
}

// ShouldIgnore reports whether a chat identified by its JID string should be
// dropped before persistence/fan-out, per the configured flags. It classifies
// purely by the JID's server component so it works on a bare string without a
// live whatsmeow client.
//
// Classification (by server):
//   - "status@broadcast"            -> status  (IgnoreStatus)
//   - server == "g.us"              -> group   (IgnoreGroups)
//   - server == "newsletter"        -> channel (IgnoreChannels)
//   - server == "broadcast"         -> broadcast list (IgnoreBroadcast)
//
// The status JID (status@broadcast) lives on the broadcast server but is
// classified as status, not broadcast, so the two flags are independent.
//
// An unparseable JID is treated as "do not ignore" (fail-open): dropping data we
// can't classify would silently lose messages, which is worse than persisting an
// oddly-shaped JID the downstream layers can still record.
func (r *IgnoreRules) ShouldIgnore(chatJID string) bool {
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return false
	}
	switch ClassifyChat(jid) {
	case ChatClassStatus:
		return r.cfg.IgnoreStatus
	case ChatClassGroup:
		return r.cfg.IgnoreGroups
	case ChatClassNewsletter:
		return r.cfg.IgnoreChannels
	case ChatClassBroadcast:
		return r.cfg.IgnoreBroadcast
	default:
		return false
	}
}

// ChatClass is the source classification of a chat JID, used both for ignore
// rules and for mapping to the domain ChatType when persisting.
type ChatClass int

const (
	ChatClassDM         ChatClass = iota // individual DM (s.whatsapp.net / lid)
	ChatClassGroup                       // g.us
	ChatClassNewsletter                  // newsletter (Channels)
	ChatClassBroadcast                   // broadcast list
	ChatClassStatus                      // status@broadcast
)

// ClassifyChat returns the ChatClass for a JID by inspecting its server (and the
// special-cased status JID). Anything not recognized as group/newsletter/
// broadcast/status is treated as a DM.
func ClassifyChat(jid types.JID) ChatClass {
	// status@broadcast is the stories feed; it shares the broadcast server but is
	// its own class so IgnoreStatus/IgnoreBroadcast stay independent.
	if jid.Server == types.BroadcastServer && jid.User == "status" {
		return ChatClassStatus
	}
	switch jid.Server {
	case types.GroupServer:
		return ChatClassGroup
	case types.NewsletterServer:
		return ChatClassNewsletter
	case types.BroadcastServer:
		return ChatClassBroadcast
	default:
		return ChatClassDM
	}
}
