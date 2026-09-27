package scenario

func Image(chat, caption, mime string, data []byte) Send {
	return Send{Chat: chat, Kind: ImageMessage, Text: caption, MimeType: mime, Data: data}
}
func Video(chat, caption, mime string, data []byte) Send {
	return Send{Chat: chat, Kind: VideoMessage, Text: caption, MimeType: mime, Data: data}
}
func Audio(chat, mime string, data []byte) Send {
	return Send{Chat: chat, Kind: AudioMessage, MimeType: mime, Data: data}
}
func Document(chat, caption, filename, mime string, data []byte) Send {
	return Send{Chat: chat, Kind: DocumentMessage, Text: caption, Filename: filename, MimeType: mime, Data: data}
}
func Sticker(chat, mime string, data []byte) Send {
	return Send{Chat: chat, Kind: StickerMessage, MimeType: mime, Data: data}
}
