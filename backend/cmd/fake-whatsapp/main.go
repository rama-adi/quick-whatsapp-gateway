package main

import (
	"log"
	"net/http"
	"os"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/fakewhatsapp"
)

func main() {
	addr := os.Getenv("FAKE_WHATSAPP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	log.Printf("fake WhatsApp control room listening on http://%s", addr)
	if err := http.ListenAndServe(addr, fakewhatsapp.NewServer()); err != nil {
		log.Fatal(err)
	}
}
