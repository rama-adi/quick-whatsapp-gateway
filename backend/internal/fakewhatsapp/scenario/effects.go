package scenario

const LastMessage = "@last"

type Effect struct {
	Chat      string
	MessageID string
	Kind      string
	Value     string
}

func (Effect) isStep() {}
func Receipt(chat, messageID, kind string) Effect {
	return Effect{Chat: chat, MessageID: messageID, Kind: kind}
}
func React(chat, messageID, emoji string) Effect {
	return Effect{Chat: chat, MessageID: messageID, Kind: "reaction", Value: emoji}
}
func Edit(chat, messageID, text string) Effect {
	return Effect{Chat: chat, MessageID: messageID, Kind: "edit", Value: text}
}
func Delete(chat, messageID string) Effect {
	return Effect{Chat: chat, MessageID: messageID, Kind: "delete"}
}

func Read(chat, messageID string) Effect      { return Receipt(chat, messageID, "read") }
func Delivered(chat, messageID string) Effect { return Receipt(chat, messageID, "delivered") }
func Played(chat, messageID string) Effect    { return Receipt(chat, messageID, "played") }
