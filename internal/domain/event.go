package domain

// Schema is the current event-envelope schema version. It is emitted as the
// "schema" field on every Event so consumers can branch on version.
const Schema = "v1"

// Event is the versioned envelope shared by both delivery transports (the NDJSON
// stream and webhooks) — §9. The same JSON shape goes out on both.
//
// Note the JSON tag mapping: the Go field Type marshals to "event" (the wire key
// in §9's example), and Timestamp is epoch-ms.
//
//	{ "schema":"v1","id":"evt_…","event":"message","session":"sess_…",
//	  "organization":"org_abc","timestamp":1719400000000,"payload":{} }
type Event struct {
	Schema       string `json:"schema"`
	ID           string `json:"id"` // evt_<ulid>, exposed to clients
	Type         string `json:"event"`
	Session      string `json:"session"`
	Organization string `json:"organization"`
	Timestamp    int64  `json:"timestamp"` // epoch ms
	Payload      any    `json:"payload"`
}

// Event catalog (§9 v1). Typed string constants for every event type so
// producers and consumers reference one source of truth instead of bare strings.
const (
	EventSessionStatus    = "session.status"
	EventAuthQR           = "auth.qr"
	EventAuthCode         = "auth.code"
	EventMessage          = "message"
	EventMessageFromMe    = "message.from_me"
	EventMessageStatus    = "message.status"
	EventMessageReaction  = "message.reaction"
	EventMessageEdited    = "message.edited"
	EventMessageRevoked   = "message.revoked"
	EventPollVote         = "poll.vote"
	EventPollRecap        = "poll.recap"
	EventPresenceUpdate   = "presence.update"
	EventGroupUpdate      = "group.update"
	EventGroupParticipant = "group.participant"
	EventChatUpdate       = "chat.update"
	EventContactUpdate    = "contact.update"
	EventCallIncoming     = "call.incoming"
	EventNewsletterUpdate = "newsletter.update"
)

// NewEvent builds an Event with the current schema, a fresh evt_ ULID, and the
// current epoch-ms timestamp. The session/organization are the originating
// WhatsApp session id and owning organization id; payload is the
// (already-normalized) type-specific body.
func NewEvent(typ, session, organization string, payload any) Event {
	return Event{
		Schema:       Schema,
		ID:           NewEventID(),
		Type:         typ,
		Session:      session,
		Organization: organization,
		Timestamp:    NowMs(),
		Payload:      payload,
	}
}
