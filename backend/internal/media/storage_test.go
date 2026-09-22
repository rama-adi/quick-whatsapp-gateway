package media

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	_ "github.com/go-sql-driver/mysql"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/crypto"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

func TestRetentionValidation(t *testing.T) {
	good := BucketInput{Name: "Archive", Endpoint: "https://s3.example.com", Bucket: "files", Region: "region"}
	for _, days := range []*int64{nil, ptr(int64(1)), ptr(int64(3650))} {
		good.RetentionDays = days
		if err := validate(good); err != nil {
			t.Fatal(err)
		}
	}
	for _, days := range []int64{0, -1, 1<<63 - 1} {
		good.RetentionDays = &days
		if validate(good) == nil {
			t.Fatalf("accepted %d", days)
		}
	}
	good.RetentionDays = nil
	good.Endpoint = "https://user:secret@example.com"
	if validate(good) == nil {
		t.Fatal("accepted embedded credentials")
	}
	for _, ip := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.0.0.1", "100.100.100.200", "::ffff:127.0.0.1"} {
		if publicIP(netip.MustParseAddr(ip).Unmap()) {
			t.Fatal("accepted internal address", ip)
		}
	}
}
func ptr[T any](v T) *T { return &v }

type fakeDownload struct{ calls int }

func (f *fakeDownload) DownloadMedia(context.Context, string, string, []byte) ([]byte, error) {
	f.calls++
	return []byte("attachment bytes"), nil
}
func TestMySQLAttachmentLifecycle(t *testing.T) {
	dsn := os.Getenv("MEDIA_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable MEDIA_TEST_DSN database qwg_media_test")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var schema string
	if err = db.QueryRow("SELECT DATABASE()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if schema != "qwg_media_test" {
		t.Fatal("requires qwg_media_test")
	}
	ctx := context.Background()
	cipher, _ := crypto.NewAESGCMFromKey(make([]byte, 32))
	org := "org_" + domain.NewULID()
	session := domain.NewSessionID()
	gateway := "gw_" + domain.NewULID()
	_, err = db.Exec(`INSERT INTO gateways(id,status,created_at,updated_at) VALUES(?,'active',1,1)`, gateway)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO wa_sessions(id,organization_id,gateway_id,status,created_at,updated_at) VALUES(?,?,?,'stopped',1,1)`, session, org, gateway)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"media_assets", "session_media_storage", "media_buckets", "event_log", "wa_sessions"} {
			_, _ = db.Exec("DELETE FROM "+table+" WHERE organization_id=?", org)
		}
		_, _ = db.Exec("DELETE FROM gateways WHERE id=?", gateway)
	})
	var mu sync.Mutex
	objects := map[string][]byte{}
	puts, deletes, heads := 0, 0, 0
	failDelete := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.RawQuery != "" && r.URL.Query().Get("versionId") != "v1" && r.URL.Query().Get("x-id") == "" {
			t.Errorf("unexpected bucket scan/query %s", r.URL.RawQuery)
		}
		switch r.Method {
		case "HEAD":
			heads++
			if data, ok := objects[r.URL.Path]; ok {
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				w.Header().Set("X-Amz-Version-Id", "v1")
			} else {
				w.WriteHeader(404)
			}
		case "PUT":
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("missing conditional write")
			}
			data, _ := io.ReadAll(r.Body)
			objects[r.URL.Path] = data
			puts++
			w.Header().Set("X-Amz-Version-Id", "v1")
		case "GET":
			data, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			_, _ = w.Write(data)
		case "DELETE":
			if failDelete {
				w.WriteHeader(403)
				return
			}
			if r.URL.Query().Get("versionId") != "v1" {
				t.Error("cleanup must target saved version")
			}
			delete(objects, r.URL.Path)
			deletes++
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected operation %s", r.Method)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	downloader := &fakeDownload{}
	published := []domain.Event{}
	failPublish := true
	var failedEventID string
	s := &Service{DB: db, Cipher: cipher, BaseURL: "https://api.example.com", Downloader: downloader, BackoffBase: time.Second, BackoffCap: time.Minute, Publish: func(_ context.Context, e domain.Event) error {
		if failPublish {
			failPublish = false
			failedEventID = e.ID
			return fmt.Errorf("temporary transport outage")
		}
		published = append(published, e)
		return nil
	}, newClient: func(b Bucket, c Credentials) *s3.Client {
		return s3.New(s3.Options{Region: b.Region, BaseEndpoint: aws.String(server.URL), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""), HTTPClient: server.Client()})
	}}
	input := BucketInput{Name: "First", Endpoint: "https://s3.example.com", Region: "test", Bucket: "files", RetentionDays: ptr(int64(1)), AccessKey: "key", SecretKey: "secret"}
	first, err := s.Save(ctx, org, "", input)
	if err != nil {
		t.Fatal(err)
	}
	input.Name = "Second"
	input.RetentionDays = nil
	second, err := s.Save(ctx, org, "", input)
	if err != nil {
		t.Fatal(err)
	}
	var encrypted []byte
	if err = db.QueryRow("SELECT credentials FROM media_buckets WHERE id=?", first.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encrypted), "secret") {
		t.Fatal("plaintext credentials")
	}
	if err = s.Link(ctx, org, session, &first.ID); err != nil {
		t.Fatal(err)
	}
	capture := func(message string) string {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"waMessageId": message, "media": domain.MediaMeta{Mimetype: "image/jpeg"}, "_mediaSource": base64.StdEncoding.EncodeToString([]byte("private descriptor"))})
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		clean, e := s.Capture(ctx, tx, store.GatewayEvent{Type: domain.EventMessage, OrganizationID: org, SessionID: session, Payload: payload})
		if e != nil {
			tx.Rollback()
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(clean), "_mediaSource") || strings.Contains(string(clean), "private descriptor") {
			t.Fatal("private descriptor leaked")
		}
		var p struct {
			Media domain.MediaMeta `json:"media"`
		}
		_ = json.Unmarshal(clean, &p)
		return p.Media.ID
	}
	id := capture("message-1")
	if id == "" {
		t.Fatal("capture missing id")
	}
	if capture("message-1") != id {
		t.Fatal("duplicate upload intent")
	}
	if err = s.Link(ctx, org, session, &second.ID); err != nil {
		t.Fatal(err)
	}
	secondID := capture("message-2")
	a, err := s.Get(ctx, org, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.BucketID != first.ID || a.ExpiresAt == nil {
		t.Fatal("relink changed existing attachment")
	}
	if err = s.Link(ctx, org, session, nil); err != nil {
		t.Fatal(err)
	}
	if capture("message-3") != "" {
		t.Fatal("unlinked session captured attachment")
	}
	if err = s.Link(ctx, "other", session, &second.ID); err == nil {
		t.Fatal("cross-org session link accepted")
	}
	if _, err = s.Get(ctx, "other", id); err == nil {
		t.Fatal("cross-org read accepted")
	}
	var originalSource []byte
	_ = db.QueryRow("SELECT source FROM media_assets WHERE id=?", id).Scan(&originalSource)
	drain := func() {
		t.Helper()
		for {
			found, e := s.step(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if !found {
				break
			}
		}
	}
	drain()
	if _, err = db.Exec("UPDATE media_assets SET next_attempt_at=0 WHERE organization_id=? AND notification IS NOT NULL", org); err != nil {
		t.Fatal(err)
	}
	drain()
	if failedEventID == "" {
		t.Fatal("notification retry not exercised")
	}
	foundRetry := false
	for _, event := range published {
		if event.ID == failedEventID {
			foundRetry = true
		}
	}
	if !foundRetry {
		t.Fatal("notification retry changed event identity")
	}
	if puts != 2 || len(published) != 2 {
		t.Fatalf("uploads=%d notifications=%d", puts, len(published))
	}
	a, err = s.Get(ctx, org, id)
	if err != nil || a.URL == "" {
		t.Fatal("ready URL missing", err)
	}
	_, err = db.Exec("UPDATE media_assets SET status='pending',source=?,notification=NULL,next_attempt_at=0 WHERE id=?", originalSource, id)
	if err != nil {
		t.Fatal(err)
	}
	drain()
	if puts != 2 || downloader.calls != 2 {
		t.Fatal("recovery reuploaded/downloaded object")
	}
	var token string
	_ = db.QueryRow("SELECT access_token FROM media_assets WHERE id=?", id).Scan(&token)
	if _, _, err = s.Open(ctx, id, strings.Repeat("0", 64)); err == nil {
		t.Fatal("invalid token accepted")
	}
	reader, _, err := s.Open(ctx, id, token)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(reader)
	reader.Close()
	if string(data) != "attachment bytes" {
		t.Fatal("content mismatch")
	}
	_, err = db.Exec("UPDATE media_assets SET expires_at=?,next_attempt_at=0 WHERE id=?", time.Now().Add(-time.Second).UnixMilli(), id)
	if err != nil {
		t.Fatal(err)
	}
	a, err = s.Get(ctx, org, id)
	if err != nil || a.URL != "" || a.Status != "expired" {
		t.Fatal("expired URL remains available")
	}
	if _, _, err = s.Open(ctx, id, token); err == nil {
		t.Fatal("expired URL accepted before cleanup")
	}
	failDelete = true
	drain()
	if deletes != 0 {
		t.Fatal("failed delete lost its retry")
	}
	failDelete = false
	if _, err = db.Exec("UPDATE media_assets SET next_attempt_at=0 WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	drain()
	if deletes != 1 {
		t.Fatalf("deletes=%d", deletes)
	}
	before := heads
	drain()
	if deletes != 1 || heads != before {
		t.Fatal("repeated cleanup hit S3")
	}
	a, err = s.Get(ctx, org, secondID)
	if err != nil || a.URL == "" || a.ExpiresAt != nil {
		t.Fatal("indefinite attachment affected")
	}
	if err = s.Delete(ctx, org, first.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Link(ctx, org, session, &second.ID); err != nil {
		t.Fatal(err)
	}
	album := domain.SendRequest{Type: domain.SendTypeAlbum, Medias: []domain.AlbumMediaPayload{{Data: base64.StdEncoding.EncodeToString([]byte("first")), Mimetype: "image/jpeg"}, {Data: base64.StdEncoding.EncodeToString([]byte("second")), Mimetype: "image/jpeg"}}}
	if err = s.CaptureOutbound(ctx, org, session, "album", album); err != nil {
		t.Fatal(err)
	}
	if err = s.CaptureOutbound(ctx, org, session, "album", album); err != nil {
		t.Fatal(err)
	}
	drain()
	messages := []domain.Message{{SessionID: session, WAMessageID: "album"}}
	if err = s.Enrich(ctx, org, messages); err != nil {
		t.Fatal(err)
	}
	if messages[0].MediaMeta == nil || len(messages[0].MediaMeta.Items) != 2 {
		t.Fatal("album files missing")
	}
	if puts != 4 {
		t.Fatal("album retry duplicated upload", puts)
	}
	for _, item := range messages[0].MediaMeta.Items {
		if item.URL == "" {
			t.Fatal("album URL missing")
		}
	}
	if err = s.Delete(ctx, org, second.ID); err == nil {
		t.Fatal("deleted connection with retained attachment")
	}
}
