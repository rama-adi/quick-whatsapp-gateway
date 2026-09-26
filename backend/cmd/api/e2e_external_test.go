package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/media"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type e2eExternal struct {
	server             *httptest.Server
	mu                 sync.Mutex
	objects            map[string][]byte
	objectTypes        map[string]string
	objectWrites       map[string]int
	loseUploadResponse bool
	rejectStorage      bool
	storageFailures    int
	hooks              [][]byte
	hookHeaders        []http.Header
}

// Only the external S3 HTTP boundary is replaced. AWS signing, upload/download,
// durable media jobs, and the gateway's protobuf descriptor handling stay real.
func e2eStorageTransport() http.RoundTripper {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	pemBytes, err := os.ReadFile(os.Getenv("SSL_CERT_FILE"))
	if err != nil {
		panic(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemBytes) {
		panic("invalid external fixture CA")
	}
	tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = tr
	return tr
}

func setupE2EExternalServices(t *testing.T, infra *e2eInfra) *e2eExternal {
	t.Helper()
	f := &e2eExternal{objects: map[string][]byte{}, objectTypes: map[string]string{}, objectWrites: map[string]int{}}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	infra.externalCA = filepath.Join(t.TempDir(), "external-ca.pem")
	if err := os.WriteFile(infra.externalCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *e2eExternal) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/hook" {
		body, _ := io.ReadAll(r.Body)
		f.hooks = append(f.hooks, body)
		f.hookHeaders = append(f.hookHeaders, r.Header.Clone())
		if len(f.hooks) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if f.rejectStorage {
		f.storageFailures++
		w.WriteHeader(http.StatusForbidden)
		return
	}
	data, exists := f.objects[r.URL.Path]
	switch r.Method {
	case http.MethodHead, http.MethodGet:
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("ETag", `"e2e-etag"`)
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	case http.MethodPut:
		if exists && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		f.objects[r.URL.Path] = body
		f.objectTypes[r.URL.Path] = r.Header.Get("Content-Type")
		f.objectWrites[r.URL.Path]++
		if f.loseUploadResponse {
			f.loseUploadResponse = false
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("ETag", `"e2e-etag"`)
	case http.MethodDelete:
		delete(f.objects, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func runE2EExternalScenarios(t *testing.T, infra *e2eInfra, gateway *e2eGateway, f *e2eExternal) {
	t.Run("media transfer recovers and enforces access and expiry", func(t *testing.T) {
		ctx, cancel := e2eContext(t)
		defer cancel()
		var bucket media.Bucket
		e2eRequireStatus(t, infra.request(t, "POST", "/api/v1/storage/buckets", e2eOrgAKey, media.BucketInput{Name: "transfer", Endpoint: f.server.URL, Region: "e2e", Bucket: "attachments", PathStyle: true, AccessKey: "e2e", SecretKey: "e2e-secret"}, &bucket, nil), 200)
		e2eRequireStatus(t, infra.request(t, "PUT", "/api/v1/sessions/"+e2eSessionID+"/storage", e2eOrgAKey, map[string]any{"bucketId": bucket.ID}, nil, nil), 200)
		f.mu.Lock()
		f.rejectStorage = true
		f.mu.Unlock()
		message, err := protojson.Marshal(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("attachment"), Mimetype: proto.String("image/jpeg"), FileLength: proto.Uint64(14), URL: proto.String("https://isolated.invalid/media"), MediaKey: make([]byte, 32), FileSHA256: make([]byte, 32), FileEncSHA256: make([]byte, 32)}})
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(map[string]any{"id": "e2e-media-inbound", "chat": e2eGroupJID, "sender": e2eSenderLID, "message": json.RawMessage(message)})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, "POST", gateway.controlURL+"/incoming", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		diagnostic, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 204 {
			t.Fatalf("incoming: %d %s", response.StatusCode, diagnostic)
		}
		var id string
		e2eEventually(t, ctx, "attachment captured durably", func() bool {
			return infra.db.QueryRowContext(ctx, `SELECT id FROM media_assets WHERE message_id='e2e-media-inbound'`).Scan(&id) == nil
		})
		e2eEventually(t, ctx, "external storage rejection observed", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.storageFailures > 0 })
		var pending media.Asset
		e2eRequireStatus(t, infra.request(t, "GET", "/api/v1/media/"+id, e2eOrgAKey, nil, &pending, nil), 200)
		if pending.Status == "ready" || pending.URL != "" {
			t.Fatalf("failed transfer exposed ready content: %+v", pending)
		}
		f.mu.Lock()
		f.rejectStorage = false
		f.mu.Unlock()
		var asset media.Asset
		e2eEventually(t, ctx, "media worker recovery", func() bool {
			return infra.request(t, "GET", "/api/v1/media/"+id, e2eOrgAKey, nil, &asset, nil) == 200 && asset.Status == "ready"
		})
		f.mu.Lock()
		stored := string(f.objects["/attachments/qwg-medias/"+e2eGroupJID+"/"+id+".jpg"])
		f.mu.Unlock()
		if stored != "isolated-media" {
			t.Fatalf("JPEG missing from chat folder: %q", stored)
		}
		e2eRequireStatus(t, infra.request(t, "GET", "/api/v1/media/"+id, e2eOrgBKey, nil, nil, nil), 404)
		e2eRequireStatus(t, infra.request(t, "GET", "/api/v1/media/"+id+"/content?token="+strings.Repeat("0", 64), "", nil, nil, nil), 404)
		response, err = http.Get(asset.URL)
		if err != nil {
			t.Fatal(err)
		}
		downloaded, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 || string(downloaded) != "isolated-media" || response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("download: status=%d body=%q headers=%v", response.StatusCode, downloaded, response.Header)
		}
		if _, err := infra.db.ExecContext(ctx, `UPDATE media_assets SET expires_at=?,next_attempt_at=0 WHERE id=?`, time.Now().Add(-time.Millisecond).UnixMilli(), id); err != nil {
			t.Fatal(err)
		}
		response, err = http.Get(asset.URL)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		e2eRequireStatus(t, response.StatusCode, 404)
		e2eEventually(t, ctx, "expired attachment object deletion", func() bool {
			var status string
			err := infra.db.QueryRowContext(ctx, `SELECT status FROM media_assets WHERE id=?`, id).Scan(&status)
			f.mu.Lock()
			defer f.mu.Unlock()
			return err == nil && status == "expired" && len(f.objects) == 0
		})
		e2eRequireStatus(t, infra.request(t, "PUT", "/api/v1/sessions/"+e2eSessionID+"/storage", e2eOrgAKey, map[string]any{"bucketId": nil}, nil, nil), 200)
	})
	t.Run("bot image echoes and API replay store once after lost upload response", func(t *testing.T) {
		ctx, cancel := e2eContext(t)
		defer cancel()
		var bucket media.Bucket
		e2eRequireStatus(t, infra.request(t, "POST", "/api/v1/storage/buckets", e2eOrgAKey, media.BucketInput{Name: "bot images", Endpoint: f.server.URL, Region: "e2e", Bucket: "attachments", PathStyle: true, AccessKey: "e2e", SecretKey: "e2e-secret"}, &bucket, nil), 200)
		binding := "/api/v1/sessions/" + e2eSessionID + "/storage"
		e2eRequireStatus(t, infra.request(t, "PUT", binding, e2eOrgAKey, map[string]any{"bucketId": bucket.ID}, nil, nil), 200)
		before := len(gateway.getCaptures(t))
		f.mu.Lock()
		f.loseUploadResponse = true
		f.objects["/attachments/user-data/keep.jpg"] = []byte("unrelated")
		f.mu.Unlock()
		request := domain.SendRequest{Type: domain.SendTypeImage, To: e2eGroupJID, Media: &domain.MediaPayload{Data: base64.StdEncoding.EncodeToString([]byte("bot-image-bytes")), Mimetype: "image/png", Caption: "bot image"}}
		status, sent := infra.send(t, e2eOrgAKey, "bot-image-storage", request)
		e2eRequireStatus(t, status, 200)
		capture := gateway.waitCaptureCount(t, before+1)[before]
		var id string
		e2eEventually(t, ctx, "bot image stored after ambiguous S3 response", func() bool {
			return infra.db.QueryRowContext(ctx, `SELECT id FROM media_assets WHERE message_id=? AND status='ready' AND notification IS NULL`, sent.WAMessageID).Scan(&id) == nil
		})
		key := "/attachments/qwg-medias/" + e2eGroupJID + "/" + id + ".png"
		body, err := json.Marshal(map[string]any{"id": sent.WAMessageID, "chat": e2eGroupJID, "sender": e2eDeviceLID, "fromMe": true, "message": json.RawMessage(capture.Message)})
		if err != nil {
			t.Fatal(err)
		}
		// Both the original own echo and its transport replay must be acknowledged.
		for _, phase := range []string{"own echo", "replayed own echo"} {
			var beforeEvents int
			if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_ingested_events WHERE session_id=? AND completed_at IS NOT NULL`, e2eSessionID).Scan(&beforeEvents); err != nil {
				t.Fatal(err)
			}
			response, err := http.Post(gateway.controlURL+"/incoming", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			e2eRequireStatus(t, response.StatusCode, 204)
			e2eEventually(t, ctx, phase+" processed", func() bool {
				var completed int
				return infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_ingested_events WHERE session_id=? AND completed_at IS NOT NULL`, e2eSessionID).Scan(&completed) == nil && completed > beforeEvents
			})
		}
		replayStatus, replay := infra.send(t, e2eOrgAKey, "bot-image-storage", request)
		e2eRequireStatus(t, replayStatus, 200)
		if replay.WAMessageID != sent.WAMessageID || !replay.Replayed {
			t.Fatalf("API replay: %+v", replay)
		}
		var assets, readyEvents, sentEvents int
		if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_assets WHERE message_id=?`, sent.WAMessageID).Scan(&assets); err != nil {
			t.Fatal(err)
		}
		if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_log WHERE type=? AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.id'))=?`, domain.EventMediaReady, id).Scan(&readyEvents); err != nil {
			t.Fatal(err)
		}
		if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_log WHERE type=? AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.waMessageId'))=?`, domain.EventMessageFromMe, sent.WAMessageID).Scan(&sentEvents); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		data, contentType, writes := string(f.objects[key]), f.objectTypes[key], f.objectWrites[key]
		f.mu.Unlock()
		if assets != 1 || readyEvents != 1 || sentEvents != 1 || writes != 1 || len(gateway.getCaptures(t)) != before+1 || data != "bot-image-bytes" || contentType != "image/png" {
			t.Fatalf("bot media duplicated or changed: assets=%d ready=%d sent=%d writes=%d captures=%d data=%q type=%q", assets, readyEvents, sentEvents, writes, len(gateway.getCaptures(t))-before, data, contentType)
		}
		if _, err := infra.db.ExecContext(ctx, `UPDATE media_assets SET expires_at=?,next_attempt_at=0 WHERE id=?`, time.Now().Add(-time.Millisecond).UnixMilli(), id); err != nil {
			t.Fatal(err)
		}
		e2eEventually(t, ctx, "bot image expiry preserves unrelated bucket data", func() bool {
			var state string
			if infra.db.QueryRowContext(ctx, `SELECT status FROM media_assets WHERE id=?`, id).Scan(&state) != nil || state != "expired" {
				return false
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			_, remains := f.objects[key]
			return !remains && string(f.objects["/attachments/user-data/keep.jpg"]) == "unrelated"
		})
		e2eRequireStatus(t, infra.request(t, "PUT", binding, e2eOrgAKey, map[string]any{"bucketId": nil}, nil, nil), 200)
	})
	t.Run("webhook retries preserve event identity and signature", func(t *testing.T) {
		ctx, cancel := e2eContext(t)
		defer cancel()
		var hook domain.Webhook
		const secret = "isolated-webhook-secret"
		e2eRequireStatus(t, infra.request(t, "POST", "/api/v1/webhooks", e2eOrgAKey, map[string]any{"url": f.server.URL + "/hook", "events": []string{"message.from_me"}, "secret": secret, "sessionId": e2eSessionID, "customHeaders": map[string]string{"X-E2E": "preserved"}}, &hook, nil), 201)
		defer infra.request(t, "DELETE", "/api/v1/webhooks/"+hook.ID, e2eOrgAKey, nil, nil, nil)
		status, sent := infra.send(t, e2eOrgAKey, "signed-webhook", domain.SendRequest{Type: domain.SendTypeSticker, To: e2eGroupJID, Media: &domain.MediaPayload{Data: base64.StdEncoding.EncodeToString([]byte("isolated-media")), Mimetype: "image/webp"}})
		e2eRequireStatus(t, status, 200)
		e2eEventually(t, ctx, "successful webhook retry", func() bool {
			var n int
			return infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_deliveries WHERE webhook_id=? AND status='delivered'`, hook.ID).Scan(&n) == nil && n == 1
		})
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.hooks) != 2 {
			t.Fatalf("one rejected then one successful delivery: got %d", len(f.hooks))
		}
		if !bytes.Equal(f.hooks[0], f.hooks[1]) {
			t.Fatal("retry changed event body")
		}
		for i, body := range f.hooks {
			var event domain.Event
			if err := json.Unmarshal(body, &event); err != nil {
				t.Fatal(err)
			}
			payload := event.Payload.(map[string]any)
			if payload["waMessageId"] != sent.WAMessageID {
				t.Fatalf("wrong delivery payload: %s", body)
			}
			sticker, ok := payload["sticker"].(map[string]any)
			if !ok || sticker["base64"] != base64.StdEncoding.EncodeToString([]byte("isolated-media")) {
				t.Fatalf("webhook sticker bytes missing: %s", body)
			}
			mac := hmac.New(sha512.New, []byte(secret))
			mac.Write(body)
			expected := hex.EncodeToString(mac.Sum(nil))
			if f.hookHeaders[i].Get("X-Webhook-Hmac") != expected || f.hookHeaders[i].Get("X-Webhook-Request-Id") != event.ID || f.hookHeaders[i].Get("X-E2E") != "preserved" || f.hookHeaders[i].Get("X-Webhook-Timestamp") == "" {
				t.Fatal(fmt.Sprintf("invalid signature/identity: %v", f.hookHeaders[i]))
			}
		}
	})
	t.Run("webhook enqueue database fault repairs one durable event", func(t *testing.T) {
		ctx, cancel := e2eContext(t)
		defer cancel()
		var hook domain.Webhook
		e2eRequireStatus(t, infra.request(t, "POST", "/api/v1/webhooks", e2eOrgAKey, map[string]any{"url": f.server.URL + "/hook", "events": []string{domain.EventMessageFromMe}, "sessionId": e2eSessionID}, &hook, nil), 201)
		defer infra.request(t, "DELETE", "/api/v1/webhooks/"+hook.ID, e2eOrgAKey, nil, nil, nil)
		if _, err := infra.db.ExecContext(ctx, `CREATE TRIGGER e2e_reject_webhook_enqueue BEFORE INSERT ON webhook_deliveries FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='e2e enqueue fault'`); err != nil {
			t.Fatal(err)
		}
		defer infra.db.Exec(`DROP TRIGGER IF EXISTS e2e_reject_webhook_enqueue`)
		before := len(gateway.getCaptures(t))
		status, result := infra.send(t, e2eOrgAKey, "enqueue-fault", domain.SendRequest{Type: domain.SendTypeText, To: e2eGroupJID, Text: "repair webhook enqueue"})
		if status != http.StatusAccepted {
			t.Fatalf("enqueue failure became terminal send: HTTP %d result %+v", status, result)
		}
		var count int
		if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_log WHERE event_id=?`, result.OutboxID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("durable event count before retry=%d", count)
		}
		if _, err := infra.db.ExecContext(ctx, `DROP TRIGGER e2e_reject_webhook_enqueue`); err != nil {
			t.Fatal(err)
		}
		e2eEventually(t, ctx, "enqueue and terminal projection repair", func() bool {
			var state string
			return infra.db.QueryRowContext(ctx, `SELECT status FROM outbox WHERE id=?`, result.OutboxID).Scan(&state) == nil && state == "sent"
		})
		if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_log WHERE event_id=?`, result.OutboxID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("retry duplicated event rows: %d", count)
		}
		if err := infra.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_deliveries WHERE event_id=? AND webhook_id=?`, result.OutboxID, hook.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 || len(gateway.getCaptures(t)) != before+1 {
			t.Fatalf("retry deliveries=%d WhatsApp captures=%d", count, len(gateway.getCaptures(t))-before)
		}
	})

}
