package scenarios

import "encoding/base64"

func tinyPNG() []byte {
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lV8AAAAASUVORK5CYII=")
	if err != nil {
		panic(err)
	}
	return data
}
func meetingNotes() []byte { return []byte("Fake WhatsApp meeting notes\n") }
