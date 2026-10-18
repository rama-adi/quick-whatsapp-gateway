package scenario

// History makes the fake phone send a history sync to its linked gateway
// sessions: the bootstrap chunk from the phone's transcripts plus Chats
// synthetic chats of Messages messages each. Force404 expires the
// downloadable chunks.
type History struct {
	Chats    int
	Messages int
	Force404 bool
}

func (History) isStep() {}

// SyncHistory sends a history sync with synthetic chats.
func SyncHistory(chats, messages int) History { return History{Chats: chats, Messages: messages} }

// ExpiredHistory sends a history sync whose downloadable chunks answer 404.
func ExpiredHistory() History { return History{Force404: true} }
