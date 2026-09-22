package domain

import (
	"crypto/rand"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// phoneJIDSuffix is the WhatsApp phone-number ("PN") JID server. A contact/identity
// key is either "<n>@lid" (a hidden LID) or "<phone>@s.whatsapp.net" (a PN JID).
const phoneJIDSuffix = "@s.whatsapp.net"

// PhoneFromJID returns the bare phone number when jid is a "@s.whatsapp.net" JID
// (e.g. "6282144201954@s.whatsapp.net" -> "6282144201954"), or nil otherwise (a
// "@lid" identifier, a group JID, or empty). The result is nil-safe for use as a
// nullable column value.
func PhoneFromJID(jid string) *string {
	user, ok := strings.CutSuffix(jid, phoneJIDSuffix)
	if !ok || user == "" {
		return nil
	}
	return &user
}

// ID prefixes used across the system. The spec's envelope examples show
// prefixed, type-tagged identifiers (e.g. "evt_01J9…", "sess_01J8…",
// "wak_<random>"). The prefix makes IDs self-describing in logs and payloads;
// the body is a (lexicographically sortable, time-ordered) ULID.
const (
	PrefixEvent       = "evt_"  // event ids exposed to clients (event_log.event_id)
	PrefixSession     = "sess_" // wa_sessions.id
	PrefixWebhook     = "wh_"   // webhooks.id
	PrefixOutbox      = "out_"  // outbox.id
	PrefixMessage     = "msg_"  // messages.id
	PrefixOAuthClient = "oac_"  // oauth_clients.id
	PrefixOAuthGrant  = "ogr_"  // oauth_grants.id
)

// monotonicEntropy is shared across all NewULID calls so that two ULIDs minted
// within the same millisecond still sort in creation order. ulid.MonotonicEntropy
// is NOT safe for concurrent use, so every read is guarded by a mutex.
var (
	entropyMu sync.Mutex
	entropy   = ulid.Monotonic(rand.Reader, 0)
)

// NewULID returns a fresh, lexicographically sortable ULID string (Crockford
// base32, 26 chars). Monotonic within a millisecond and safe for concurrent use.
func NewULID() string {
	entropyMu.Lock()
	defer entropyMu.Unlock()
	// ulid.MustNew with shared monotonic entropy: panics only on entropy read
	// failure (crypto/rand), which is treated as unrecoverable.
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}

// NewPrefixedID returns prefix + NewULID(), e.g. "evt_01J9...".
func NewPrefixedID(prefix string) string { return prefix + NewULID() }

// NewEventID, NewSessionID, etc. are convenience constructors for each
// well-known prefixed identifier kind.
func NewEventID() string       { return NewPrefixedID(PrefixEvent) }
func NewSessionID() string     { return NewPrefixedID(PrefixSession) }
func NewWebhookID() string     { return NewPrefixedID(PrefixWebhook) }
func NewOutboxID() string      { return NewPrefixedID(PrefixOutbox) }
func NewMessageID() string     { return NewPrefixedID(PrefixMessage) }
func NewOAuthClientID() string { return NewPrefixedID(PrefixOAuthClient) }
func NewOAuthGrantID() string  { return NewPrefixedID(PrefixOAuthGrant) }

// NowMs returns the current time as epoch milliseconds — the canonical
// timestamp unit for every BIGINT column in §5.
func NowMs() int64 { return time.Now().UnixMilli() }
