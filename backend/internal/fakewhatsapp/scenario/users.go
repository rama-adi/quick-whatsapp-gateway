package scenario

// User is a reusable fake WhatsApp peer. It carries both wire identity and the
// push name the gateway sees on incoming messages.
type User struct {
	Phone string
	Name  string
}

func Person(phone, name string) User { return User{Phone: phone, Name: name} }
func (u User) JID() string           { return u.Phone + "@s.whatsapp.net" }
func (u User) Says(text string) Send { return Text(u.JID(), text).From(u.JID()).Named(u.Name) }
func (u User) SaysIn(group GroupRef, text string) Send {
	return Text(group.Chat(), text).From(u.JID()).Named(u.Name)
}
func (u User) SendsImage(caption, mime string, data []byte) Send {
	return Image(u.JID(), caption, mime, data).From(u.JID()).Named(u.Name)
}
func (u User) SendsDocument(caption, filename, mime string, data []byte) Send {
	return Document(u.JID(), caption, filename, mime, data).From(u.JID()).Named(u.Name)
}
func (u User) SharesLocation(name string, latitude, longitude float64) Send {
	return Location(u.JID(), name, latitude, longitude).From(u.JID()).Named(u.Name)
}
func (u User) SharesContact(name, phone string) Send {
	return Contact(u.JID(), name, phone).From(u.JID()).Named(u.Name)
}
func (u User) Asks(question string, options ...string) Send {
	return Poll(u.JID(), question, options...).From(u.JID()).Named(u.Name)
}

func (u User) Replies(quotedID, text string) Send {
	return Reply(u.JID(), quotedID, text).From(u.JID()).Named(u.Name)
}
func (u User) RepliesIn(group GroupRef, quotedID, text string) Send {
	return Reply(group.Chat(), quotedID, text).From(u.JID()).Named(u.Name)
}
