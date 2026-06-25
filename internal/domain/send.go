package domain

// Send message type discriminators for the unified send endpoint
// (POST /sessions/{id}/messages, §11). Media types parse but the handler
// returns 501 in v1.
const (
	SendTypeText     = "text"
	SendTypePoll     = "poll"
	SendTypeLocation = "location"
	SendTypeContact  = "contact"
	// v1 -> 501:
	SendTypeImage    = "image"
	SendTypeVideo    = "video"
	SendTypeAudio    = "audio"
	SendTypeDocument = "document"
	SendTypeSticker  = "sticker"
)

// ContactCard is the payload for a type:"contact" send (a shared contact card).
type ContactCard struct {
	Name  string `json:"name,omitempty"`
	Phone string `json:"phone,omitempty"`
	// VCard, when present, is sent verbatim; otherwise one is built from Name/Phone.
	VCard string `json:"vcard,omitempty"`
}

// SendRequest is the single discriminated inbound body for the unified send
// endpoint (§11). The masterplan uses one body shape discriminated on Type, so
// this is intentionally a flat struct with all per-type fields optional; the
// handler validates the subset required for each Type. JSON tags follow the §11
// camelCase examples.
type SendRequest struct {
	Type string `json:"type"` // one of the SendType* constants
	To   string `json:"to"`   // recipient JID, e.g. "628123@s.whatsapp.net" or "12036@g.us"

	// text
	Text     string   `json:"text,omitempty"`
	ReplyTo  string   `json:"replyTo,omitempty"`  // quoted wa_message_id
	Mentions []string `json:"mentions,omitempty"` // mentioned JIDs

	// poll
	Name            string   `json:"name,omitempty"`            // also used as location label
	Options         []string `json:"options,omitempty"`         // poll choices
	SelectableCount int      `json:"selectableCount,omitempty"` // max selectable options

	// location
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`

	// contact
	Contact *ContactCard `json:"contact,omitempty"`
}
