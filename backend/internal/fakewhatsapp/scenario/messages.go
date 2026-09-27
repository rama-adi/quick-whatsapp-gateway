package scenario

type MessageKind string

const (
	TextMessage     MessageKind = "text"
	ImageMessage    MessageKind = "image"
	VideoMessage    MessageKind = "video"
	AudioMessage    MessageKind = "audio"
	DocumentMessage MessageKind = "document"
	StickerMessage  MessageKind = "sticker"
	LocationMessage MessageKind = "location"
	ContactMessage  MessageKind = "contact"
	PollMessage     MessageKind = "poll"
	ButtonsMessage  MessageKind = "buttons"
	ListMessage     MessageKind = "list"
)

type Send struct {
	Chat         string
	Sender       string
	SenderName   string
	QuoteID      string
	Kind         MessageKind
	Text         string
	Filename     string
	MimeType     string
	Data         []byte
	Latitude     float64
	Longitude    float64
	ContactName  string
	ContactPhone string
	PollOptions  []string
	Buttons      []string
	ListRows     []string
}

func (Send) isStep()                  {}
func (s Send) From(jid string) Send   { s.Sender = jid; return s }
func (s Send) Named(name string) Send { s.SenderName = name; return s }
func Text(chat, text string) Send     { return Send{Chat: chat, Kind: TextMessage, Text: text} }
func Location(chat, name string, latitude, longitude float64) Send {
	return Send{Chat: chat, Kind: LocationMessage, Text: name, Latitude: latitude, Longitude: longitude}
}
func Contact(chat, name, phone string) Send {
	return Send{Chat: chat, Kind: ContactMessage, ContactName: name, ContactPhone: phone}
}
func Poll(chat, question string, options ...string) Send {
	return Send{Chat: chat, Kind: PollMessage, Text: question, PollOptions: options}
}
func Buttons(chat, body string, choices ...string) Send {
	return Send{Chat: chat, Kind: ButtonsMessage, Text: body, Buttons: choices}
}
func List(chat, body string, rows ...string) Send {
	return Send{Chat: chat, Kind: ListMessage, Text: body, ListRows: rows}
}

// Reply carries a WhatsApp text context with the quoted message ID. The gateway
// can resolve the quoted body from its persisted message when it is available.
func Reply(chat, quotedID, text string) Send {
	return Send{Chat: chat, Kind: TextMessage, Text: text, QuoteID: quotedID}
}
