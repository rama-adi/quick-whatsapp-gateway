package apitypes

import "github.com/ramaadi/quick-whatsapp-gateway/internal/domain"

// This file is the typed event catalog — the source of truth for both the runtime
// wire payloads (internal/wa/events aliases these types) and the generated OpenAPI
// (cmd/genopenapi registers them as component schemas + an OpenAPI 3.1 `webhooks`
// section). Typing the payloads (the hand-written spec left them
// `additionalProperties: true`) means webhook receivers and the realtime
// WebSocket client get a fully described, unambiguous shape per event type.
//
// Every event the gateway emits is delivered two ways with the SAME envelope:
//   - as the JSON body of a webhook POST (one event per request), and
//   - as a discrete JSON message over the realtime WebSocket on the central router.

// --- Wire payloads (the `payload` field of an event) ---

// SessionStatusPayload is the payload of a `session.status` event: the session's
// connection lifecycle changed.
type SessionStatusPayload struct {
	Status string `json:"status" doc:"New session status. One of: starting (connecting), scan_qr_code (waiting for a QR scan or pairing code), working (connected and ready to send/receive), failed (connection failed; will retry), stopped (intentionally stopped), logged_out (the WhatsApp account unlinked the device — must re-pair)." enum:"starting,scan_qr_code,working,failed,stopped,logged_out" example:"working"`
}

// AuthQRPayload is the payload of an `auth.qr` event: a fresh QR code to render so
// the user can link the device. A new code arrives roughly every 20s until paired.
type AuthQRPayload struct {
	Code string `json:"code" doc:"The QR code content to render as a QR image for the user to scan in WhatsApp → Linked devices." example:"2@abc123..."`
}

// AuthCodePayload is the payload of an `auth.code` event: pairing succeeded and the
// device is now linked to this WhatsApp account.
type AuthCodePayload struct {
	JID          string `json:"jid,omitempty" doc:"The linked phone JID (e.g. 6281234567890@s.whatsapp.net)." example:"6281234567890@s.whatsapp.net"`
	LID          string `json:"lid,omitempty" doc:"The linked-device identifier (LID) WhatsApp assigned to this device."`
	BusinessName string `json:"businessName,omitempty" doc:"The account's WhatsApp Business display name, if it is a business account."`
	Platform     string `json:"platform,omitempty" doc:"The device platform WhatsApp reports (e.g. android, iphone, web)."`
}

// LocationData is the body of a location message.
type LocationData struct {
	Latitude  float64 `json:"latitude" doc:"Latitude in decimal degrees." example:"-6.2"`
	Longitude float64 `json:"longitude" doc:"Longitude in decimal degrees." example:"106.8"`
	Name      string  `json:"name,omitempty" doc:"Optional place name."`
	Address   string  `json:"address,omitempty" doc:"Optional street address."`
}

// ContactData is the body of a shared contact-card message (the vCard verbatim).
type ContactData struct {
	DisplayName string `json:"displayName,omitempty" doc:"The contact's display name."`
	VCard       string `json:"vcard,omitempty" doc:"The raw vCard for the shared contact."`
}

// PollData is the body of a poll-creation message.
type PollData struct {
	Name            string   `json:"name" doc:"The poll question."`
	Options         []string `json:"options" doc:"The poll answer options, in order."`
	SelectableCount int      `json:"selectableCount" doc:"How many options a voter may select (1 = single choice)." example:"1"`
}

// MessagePayload is the payload shared by the message-family events — `message`
// (inbound), `message.from_me` (a message this account sent, including from other
// linked devices), `message.reaction`, `message.edited`, `message.revoked`, and
// `poll.vote`. One flat shape discriminated by the surrounding event type; only
// the fields relevant to that type are populated.
type MessagePayload struct {
	WAMessageID     string            `json:"waMessageId" doc:"WhatsApp's message id (stanza id), unique within the chat." example:"3EB0..."`
	ChatJID         string            `json:"chatJid" doc:"JID of the chat the message belongs to (a user, group, or broadcast list)." example:"6281234567890@s.whatsapp.net"`
	SenderJID       string            `json:"senderJid,omitempty" doc:"JID of the sender (set for group messages; for DMs it equals the chat)."`
	SenderLID       string            `json:"senderLid,omitempty" doc:"The sender's linked-device id, when available."`
	FromMe          bool              `json:"fromMe" doc:"True if this account authored the message (the message.from_me event)."`
	Type            string            `json:"type" doc:"Content type of the message: text, location, contact, poll, reaction, edit, revoke, media, and others." example:"text"`
	Body            string            `json:"body,omitempty" doc:"Text body, for text and caption-bearing messages." example:"Hello!"`
	QuotedMessageID string            `json:"quotedMessageId,omitempty" doc:"If this message quotes/replies to another, that message's id."`
	Mentions        []string          `json:"mentions,omitempty" doc:"JIDs mentioned in the message body."`
	HasMedia        bool              `json:"hasMedia" doc:"True if the message carried media. The media itself is metadata-only in v1 (no download)."`
	Media           *domain.MediaMeta `json:"media" doc:"Media descriptor. Always null in v1 (metadata-only); see hasMedia."`
	Timestamp       int64             `json:"timestamp" doc:"When WhatsApp timestamped the message, in epoch milliseconds."`
	PushName        string            `json:"pushName,omitempty" doc:"The sender's WhatsApp display (push) name at send time."`
	Reaction        string            `json:"reaction,omitempty" doc:"For message.reaction: the emoji reacted with; empty string means the reaction was removed."`
	TargetID        string            `json:"targetId,omitempty" doc:"For reaction/edit/revoke: the id of the message being reacted to, edited, or revoked."`
	Location        *LocationData     `json:"location,omitempty" doc:"Set when type is location."`
	Contact         *ContactData      `json:"contact,omitempty" doc:"Set when type is contact."`
	Poll            *PollData         `json:"poll,omitempty" doc:"Set when type is poll (poll creation)."`
	SelectedHashes  []string          `json:"selectedHashes,omitempty" doc:"For poll.vote: the encrypted option hashes the voter selected."`
}

// MessageStatusPayload is the payload of a `message.status` event: a delivery
// receipt advanced one or more messages' status.
type MessageStatusPayload struct {
	ChatJID    string   `json:"chatJid" doc:"JID of the chat the receipt is for."`
	SenderJID  string   `json:"senderJid,omitempty" doc:"JID of the participant the receipt came from (group chats)."`
	MessageIDs []string `json:"messageIds" doc:"The message ids whose status advanced."`
	Status     string   `json:"status" doc:"New delivery status, in progression order: sent (left this device) → delivered (reached the recipient device) → read (recipient opened it) → played (voice/video note played). May also be failed." enum:"pending,sent,delivered,read,played,failed" example:"delivered"`
	Timestamp  int64    `json:"timestamp" doc:"When the receipt was generated, epoch milliseconds."`
}

// PresencePayload is the payload of a `presence.update` event (a contact's online
// or typing state, or a chat presence such as typing/recording).
type PresencePayload struct {
	ChatJID     string `json:"chatJid,omitempty" doc:"Chat the presence applies to, for chat (typing) presence."`
	From        string `json:"from" doc:"JID whose presence changed."`
	State       string `json:"state" doc:"Presence state: available (online), unavailable (offline), composing (typing), or paused (stopped typing)." enum:"available,unavailable,composing,paused" example:"composing"`
	Media       string `json:"media,omitempty" doc:"For chat presence, the kind being composed: text or audio." enum:"text,audio"`
	Unavailable bool   `json:"unavailable,omitempty" doc:"True when the contact went offline."`
	LastSeen    int64  `json:"lastSeen,omitempty" doc:"Last-seen time in epoch milliseconds, when the contact shares it."`
}

// GroupPayload is the payload of `group.update` (group metadata changed) and
// `group.participant` (members joined/left/were promoted/demoted) events.
type GroupPayload struct {
	GroupJID      string   `json:"groupJid" doc:"JID of the group." example:"123456789-987654321@g.us"`
	Subject       string   `json:"subject,omitempty" doc:"New group subject (name), when it changed."`
	Description   string   `json:"description,omitempty" doc:"New group description, when it changed."`
	Sender        string   `json:"sender,omitempty" doc:"JID of the member who made the change."`
	IsAnnounce    *bool    `json:"isAnnounce,omitempty" doc:"Announce mode: when true only admins can send messages."`
	IsLocked      *bool    `json:"isLocked,omitempty" doc:"Locked mode: when true only admins can edit group info."`
	NewInviteLink string   `json:"newInviteLink,omitempty" doc:"New invite link, when it was reset."`
	Reason        string   `json:"reason,omitempty" doc:"Reason/context for the change, when WhatsApp provides one."`
	Join          []string `json:"join,omitempty" doc:"JIDs that joined the group."`
	Leave         []string `json:"leave,omitempty" doc:"JIDs that left or were removed."`
	Promote       []string `json:"promote,omitempty" doc:"JIDs promoted to admin."`
	Demote        []string `json:"demote,omitempty" doc:"JIDs demoted from admin."`
	Timestamp     int64    `json:"timestamp,omitempty" doc:"When the change happened, epoch milliseconds."`
}

// ChatUpdatePayload is the payload of a `chat.update` event (currently a chat's
// profile picture changed).
type ChatUpdatePayload struct {
	ChatJID   string `json:"chatJid" doc:"JID of the chat that changed."`
	Change    string `json:"change" doc:"What changed; currently picture." example:"picture"`
	PictureID string `json:"pictureId,omitempty" doc:"Id of the new profile picture, when set."`
	Removed   bool   `json:"removed,omitempty" doc:"True if the picture was removed rather than changed."`
}

// ContactUpdatePayload is the payload of a `contact.update` event (a contact's
// push name or saved name changed).
type ContactUpdatePayload struct {
	JID       string `json:"jid" doc:"JID of the contact that changed."`
	PushName  string `json:"pushName,omitempty" doc:"The contact's current WhatsApp display (push) name."`
	FullName  string `json:"fullName,omitempty" doc:"The full name from the address book, when known."`
	FirstName string `json:"firstName,omitempty" doc:"The first name from the address book, when known."`
}

// CallPayload is the payload of a `call.incoming` event.
type CallPayload struct {
	CallID    string `json:"callId" doc:"WhatsApp's call id."`
	From      string `json:"from" doc:"JID of the caller."`
	Timestamp int64  `json:"timestamp" doc:"When the call came in, epoch milliseconds."`
	IsGroup   bool   `json:"isGroup" doc:"True for a group call."`
	GroupJID  string `json:"groupJid,omitempty" doc:"JID of the group, for a group call."`
}

// NewsletterPayload is the payload of a `newsletter.update` event (a channel the
// account follows changed state).
type NewsletterPayload struct {
	JID    string `json:"jid" doc:"JID of the newsletter/channel."`
	Action string `json:"action" doc:"What happened: join, leave, or mute." enum:"join,leave,mute" example:"join"`
}

// --- Typed event envelopes (the documented webhook/WS messages) ---

// EventMeta is the common envelope every event shares. It is embedded into each
// typed event so the generated schemas all carry the same documented header.
type EventMeta struct {
	Schema       string `json:"schema" doc:"Envelope schema version, so consumers can adapt if the shape changes." enum:"v1" example:"v1"`
	ID           string `json:"id" doc:"Unique event id. Webhook deliveries repeat it in the X-Webhook-Request-Id header so receivers can drop duplicate redeliveries; the realtime client uses it as the ?since resume cursor." example:"evt_01J9ZX..."`
	Session      string `json:"session" doc:"Id of the WhatsApp session the event came from." example:"wa_sess_01J9..."`
	Organization string `json:"organization" doc:"Id of the organization that owns the session." example:"org_01J9..."`
	Timestamp    int64  `json:"timestamp" doc:"When the event happened, in epoch milliseconds." example:"1719400000000"`
}

// MessageEvent is the envelope for the message-family events.
type MessageEvent struct {
	EventMeta
	Event   string         `json:"event" doc:"The event type." enum:"message,message.from_me,message.reaction,message.edited,message.revoked,poll.vote" example:"message"`
	Payload MessagePayload `json:"payload"`
}

// MessageStatusEvent is the envelope for a delivery-receipt event.
type MessageStatusEvent struct {
	EventMeta
	Event   string               `json:"event" enum:"message.status" example:"message.status"`
	Payload MessageStatusPayload `json:"payload"`
}

// SessionStatusEvent is the envelope for a session lifecycle event.
type SessionStatusEvent struct {
	EventMeta
	Event   string               `json:"event" enum:"session.status" example:"session.status"`
	Payload SessionStatusPayload `json:"payload"`
}

// AuthQREvent is the envelope for a QR-code event.
type AuthQREvent struct {
	EventMeta
	Event   string        `json:"event" enum:"auth.qr" example:"auth.qr"`
	Payload AuthQRPayload `json:"payload"`
}

// AuthCodeEvent is the envelope for a pairing-success event.
type AuthCodeEvent struct {
	EventMeta
	Event   string          `json:"event" enum:"auth.code" example:"auth.code"`
	Payload AuthCodePayload `json:"payload"`
}

// PresenceEvent is the envelope for a presence-update event.
type PresenceEvent struct {
	EventMeta
	Event   string          `json:"event" enum:"presence.update" example:"presence.update"`
	Payload PresencePayload `json:"payload"`
}

// GroupEvent is the envelope for group metadata/participant events.
type GroupEvent struct {
	EventMeta
	Event   string       `json:"event" enum:"group.update,group.participant" example:"group.update"`
	Payload GroupPayload `json:"payload"`
}

// ChatUpdateEvent is the envelope for a chat-update event.
type ChatUpdateEvent struct {
	EventMeta
	Event   string            `json:"event" enum:"chat.update" example:"chat.update"`
	Payload ChatUpdatePayload `json:"payload"`
}

// ContactUpdateEvent is the envelope for a contact-update event.
type ContactUpdateEvent struct {
	EventMeta
	Event   string               `json:"event" enum:"contact.update" example:"contact.update"`
	Payload ContactUpdatePayload `json:"payload"`
}

// CallEvent is the envelope for an incoming-call event.
type CallEvent struct {
	EventMeta
	Event   string      `json:"event" enum:"call.incoming" example:"call.incoming"`
	Payload CallPayload `json:"payload"`
}

// NewsletterEvent is the envelope for a newsletter/channel event.
type NewsletterEvent struct {
	EventMeta
	Event   string            `json:"event" enum:"newsletter.update" example:"newsletter.update"`
	Payload NewsletterPayload `json:"payload"`
}

// EventTypeSchemas returns one zero value of every typed event envelope, in a
// stable order. cmd/genopenapi registers these as component schemas and builds the
// OpenAPI 3.1 `webhooks` section (a oneOf over them, discriminated by `event`).
func EventTypeSchemas() []any {
	return []any{
		MessageEvent{}, MessageStatusEvent{}, SessionStatusEvent{},
		AuthQREvent{}, AuthCodeEvent{}, PresenceEvent{}, GroupEvent{},
		ChatUpdateEvent{}, ContactUpdateEvent{}, CallEvent{}, NewsletterEvent{},
	}
}
