package domain

// Send message type discriminators for the unified send endpoint
// (POST /sessions/{id}/messages, §11).
const (
	SendTypeText     = "text"
	SendTypePoll     = "poll"
	SendTypeLocation = "location"
	SendTypeContact  = "contact"
	// Media sends (file supplied inline as base64 via the media field):
	SendTypeImage    = "image"
	SendTypeVideo    = "video"
	SendTypeAudio    = "audio"
	SendTypeDocument = "document"
	SendTypeSticker  = "sticker"
)

// MediaPayload is the payload for a media send (image/video/audio/document/
// sticker). The file is supplied inline as base64 so the JSON endpoint stays
// self-contained (no server-side URL fetching). The gateway decodes it, uploads
// it to WhatsApp, and attaches the resulting media reference to the message.
type MediaPayload struct {
	Data     string `json:"data" doc:"The file's bytes, base64-encoded (standard encoding, padding optional). Required for media sends." example:"iVBORw0KGgoAAAANSUhEUgAA...="`
	Mimetype string `json:"mimetype,omitempty" doc:"The file's MIME type, e.g. image/jpeg. Detected from the bytes when omitted." example:"image/jpeg"`
	Caption  string `json:"caption,omitempty" doc:"Optional caption shown beneath the media." example:"Here you go!"`
	Filename string `json:"filename,omitempty" doc:"Optional original filename (used for documents). Optional." example:"photo.jpg"`
}

// ContactCard is the payload for a type:"contact" send (a shared contact card).
type ContactCard struct {
	Name  string `json:"name,omitempty" doc:"The shared contact's display name. Used to build a vCard when vcard is not supplied. Optional." example:"Alice"`
	Phone string `json:"phone,omitempty" doc:"The shared contact's phone number in plain digits. Used to build a vCard when vcard is not supplied. Optional." example:"6281234567890"`
	// VCard, when present, is sent verbatim; otherwise one is built from Name/Phone.
	VCard string `json:"vcard,omitempty" doc:"A full vCard string. If set, it is sent verbatim instead of building one from name/phone. Optional." example:"BEGIN:VCARD\nVERSION:3.0\nFN:Alice\nTEL:+6281234567890\nEND:VCARD"`
}

// SendRequest is the single discriminated inbound body for the unified send
// endpoint (§11). The masterplan uses one body shape discriminated on Type, so
// this is intentionally a flat struct with all per-type fields optional; the
// handler validates the subset required for each Type. JSON tags follow the §11
// camelCase examples.
type SendRequest struct {
	Type string `json:"type" enum:"text,poll,location,contact,image,video,audio,document,sticker" doc:"Which kind of message to send. Determines which other fields are required. **text** uses text (+ optional replyTo/mentions); **poll** uses name (question) + options + selectableCount; **location** uses latitude + longitude + optional name (label); **contact** uses contact. The media types **image**, **video**, **audio**, **document**, **sticker** use media (a base64 file + optional caption/filename, replyTo, mentions)." example:"text"`
	To   string `json:"to" doc:"The recipient's JID: a user JID for a direct message (e.g. 6281234567890@s.whatsapp.net) or a group JID for a group (e.g. 120363021234567890@g.us). Required." example:"6281234567890@s.whatsapp.net"`

	// text
	Text     string   `json:"text,omitempty" doc:"The message text. Required for type text; ignored otherwise." example:"Hello there!"`
	ReplyTo  string   `json:"replyTo,omitempty" doc:"Id of the message this one quotes/replies to (a wa_message_id). Optional." example:"3EB0C431C26A1916E001"`
	Mentions []string `json:"mentions,omitempty" doc:"JIDs to @-mention in the message. Optional." example:"[\"6289876543210@s.whatsapp.net\"]"`

	// poll
	Name            string   `json:"name,omitempty" doc:"For a poll, the poll question; for a location, the place label. Required for poll; optional for location." example:"Lunch on Friday?"`
	Options         []string `json:"options,omitempty" doc:"The poll's answer options. Required for type poll." example:"[\"Yes\",\"No\",\"Maybe\"]"`
	SelectableCount int      `json:"selectableCount,omitempty" doc:"How many options a voter may pick in the poll (1 = single choice). Used for type poll." example:"1"`
	PollEndTime     int64    `json:"pollEndTime,omitempty" doc:"Optional poll closing time as epoch milliseconds. Used for type poll when WhatsApp supports poll end times." example:"1719662400000"`
	PollHideVotes   bool     `json:"pollHideVotes,omitempty" doc:"When true, ask WhatsApp to hide participant names in the poll vote list. Used for type poll." example:"true"`

	// location
	Latitude  float64 `json:"latitude,omitempty" doc:"Latitude of the shared location in decimal degrees. Required for type location." example:"-6.2"`
	Longitude float64 `json:"longitude,omitempty" doc:"Longitude of the shared location in decimal degrees. Required for type location." example:"106.816666"`

	// contact
	Contact *ContactCard `json:"contact,omitempty" doc:"The contact card to share. Required for type contact."`

	// media (image/video/audio/document/sticker)
	Media *MediaPayload `json:"media,omitempty" doc:"The media file to send (base64). Required for the media types (image/video/audio/document/sticker). Caption, replyTo, and mentions apply."`
}
