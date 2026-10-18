package fakewhatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	sqlitestore "github.com/rama-adi/quick-whatsapp-gateway/internal/wa/store/sqlite"
)

func postJSON(t *testing.T, url string, body, out any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		t.Fatalf("POST %s: %d", url, resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
}

// pairedHistoryClient pairs a fake client with history enabled and returns it
// with a channel of the history notifications it receives.
func pairedHistoryClient(t *testing.T, cfg Config) (*Client, *httptest.Server, chan *events.Message) {
	t.Helper()
	ctx := context.Background()
	ks, err := sqlitestore.Open(ctx, "file:"+filepath.Join(t.TempDir(), "device.db")+"?_pragma=foreign_keys(1)", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ks.Close() })
	srv := httptest.NewServer(NewServer())
	t.Cleanup(srv.Close)
	postJSON(t, srv.URL+"/v1/numbers/555000001", cfg, nil)
	client := NewClient(ks.NewDevice(), srv.URL)
	t.Cleanup(client.Disconnect)
	history := make(chan *events.Message, 64)
	paired := make(chan struct{}, 1)
	client.AddEventHandler(func(evt any) {
		switch e := evt.(type) {
		case *events.PairSuccess:
			paired <- struct{}{}
		case *events.Message:
			if e.Info.IsFromMe && e.Message.GetProtocolMessage().GetHistorySyncNotification() != nil {
				history <- e
			}
		}
	})
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PairPhone(ctx, "555000001", true, whatsmeow.PairClientChrome, "Chrome (test)"); err != nil {
		t.Fatal(err)
	}
	postJSON(t, srv.URL+"/v1/sessions/"+client.key+"/confirm", ConfirmRequest{Number: "555000001"}, nil)
	select {
	case <-paired:
	case <-time.After(5 * time.Second):
		t.Fatal("pairing did not complete")
	}
	return client, srv, history
}

func nextHistory(t *testing.T, ch chan *events.Message) *events.Message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no history notification")
		return nil
	}
}

func TestFakeHistorySyncAfterPairing(t *testing.T) {
	ctx := context.Background()
	client, srv, ch := pairedHistoryClient(t, Config{HistorySync: true, HistoryChats: 12, HistoryMessages: 30})
	boot := nextHistory(t, ch)
	notif := boot.Message.GetProtocolMessage().GetHistorySyncNotification()
	if len(notif.GetInitialHistBootstrapInlinePayload()) == 0 || notif.GetSyncType() != waE2E.HistorySyncType_INITIAL_BOOTSTRAP {
		t.Fatalf("bootstrap notification = %+v", notif)
	}
	if boot.Info.Chat.String() != "555000001@s.whatsapp.net" {
		t.Fatalf("notification chat = %s", boot.Info.Chat)
	}
	blob, err := client.DownloadHistorySync(ctx, notif, true)
	if err != nil {
		t.Fatal(err)
	}
	if blob.GetSyncType() != waHistorySync.HistorySync_INITIAL_BOOTSTRAP || len(blob.GetConversations()) != 2 || len(blob.GetPushnames()) != 2 {
		t.Fatalf("bootstrap blob: type=%s conversations=%d", blob.GetSyncType(), len(blob.GetConversations()))
	}
	first := blob.GetConversations()[0]
	chat, _ := types.ParseJID(first.GetID())
	parsed, err := client.ParseWebMessage(chat, first.GetMessages()[0].GetMessage())
	if err != nil || parsed.Message.GetConversation() == "" {
		t.Fatalf("parse: %v %+v", err, parsed)
	}
	if err := client.SendProtocolMessageReceipt(ctx, types.MessageID(boot.Info.ID), types.ReceiptTypeHistorySync); err != nil {
		t.Fatal(err)
	}

	// 12 extra chats → two RECENT chunks of up to 10 chats; the last is 100%.
	var total int
	for i := 0; i < 2; i++ {
		evt := nextHistory(t, ch)
		n := evt.Message.GetProtocolMessage().GetHistorySyncNotification()
		if n.GetDirectPath() == "" || len(n.GetInitialHistBootstrapInlinePayload()) != 0 {
			t.Fatalf("recent chunk should be downloadable: %+v", n)
		}
		recent, err := client.DownloadHistorySync(ctx, n, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range recent.GetConversations() {
			total += len(c.GetMessages())
		}
		if i == 1 && recent.GetProgress() != 100 {
			t.Fatalf("last chunk progress = %d", recent.GetProgress())
		}
		if err := client.DeleteMedia(ctx, whatsmeow.MediaHistory, n.GetDirectPath(), nil, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := client.DownloadHistorySync(ctx, n, true); !errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) {
			t.Fatalf("deleted blob still downloadable: %v", err)
		}
	}
	if total != 12*30 {
		t.Fatalf("synthetic messages = %d", total)
	}
	var state State
	resp, err := http.Get(srv.URL + "/v1/state")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_ = json.NewDecoder(resp.Body).Decode(&state)
	kinds := map[string]int{}
	for _, op := range state.Operations {
		kinds[op.Kind]++
	}
	if kinds["history_receipt"] != 1 || kinds["delete_media"] != 2 || kinds["history_download"] != 4 {
		t.Fatalf("operations = %v", kinds)
	}
}

func TestFakeHistoryForced404(t *testing.T) {
	ctx := context.Background()
	client, srv, ch := pairedHistoryClient(t, Config{})
	var result HistoryResponse
	postJSON(t, srv.URL+"/v1/numbers/555000001/history", HistoryRequest{Force404: true}, &result)
	if result.Notifications != 2 || result.Delivered != 2 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := client.DownloadHistorySync(ctx, nextHistory(t, ch).Message.GetProtocolMessage().GetHistorySyncNotification(), true); err != nil {
		t.Fatalf("inline bootstrap must not expire: %v", err)
	}
	n := nextHistory(t, ch).Message.GetProtocolMessage().GetHistorySyncNotification()
	if _, err := client.DownloadHistorySync(ctx, n, true); !errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) {
		t.Fatalf("expected 404, got %v", err)
	}
	if err := client.SendHistorySyncServerErrorReceipt(ctx, "X", n.GetMediaKey()); err != nil {
		t.Fatal(err)
	}
}
