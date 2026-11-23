package media

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/crypto"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

type failingDownload struct {
	err   error
	calls int
}

func (f *failingDownload) DownloadMedia(context.Context, string, string, []byte) ([]byte, error) {
	f.calls++
	return nil, f.err
}

// mediaWorkerFixture prepares a session linked to a bucket on a fake S3 that
// has no objects, and returns a capture helper.
func mediaWorkerFixture(t *testing.T, downloader Downloader) (*Service, *sql.DB, func(string) string) {
	t.Helper()
	dsn := os.Getenv("MEDIA_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable MEDIA_TEST_DSN database qwg_media_test")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var schema string
	if err = db.QueryRow("SELECT DATABASE()").Scan(&schema); err != nil || schema != "qwg_media_test" {
		t.Fatal("requires qwg_media_test")
	}
	ctx := context.Background()
	cipher, _ := crypto.NewAESGCMFromKey(make([]byte, 32))
	org := "org_" + domain.NewULID()
	session := domain.NewSessionID()
	gateway := "gw_" + domain.NewULID()
	if _, err = db.Exec(`INSERT INTO gateways(id,status,created_at,updated_at) VALUES(?,'active',1,1)`, gateway); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO wa_sessions(id,organization_id,gateway_id,created_at,updated_at) VALUES(?,?,?,1,1)`, session, org, gateway); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"media_assets", "session_media_storage", "media_buckets", "event_log", "wa_sessions"} {
			_, _ = db.Exec("DELETE FROM "+table+" WHERE organization_id=?", org)
		}
		_, _ = db.Exec("DELETE FROM gateways WHERE id=?", gateway)
	})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Method == "HEAD" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("X-Amz-Version-Id", "v1")
	}))
	t.Cleanup(server.Close)
	s := &Service{DB: db, Cipher: cipher, BaseURL: "https://api.example.com", Downloader: downloader,
		BackoffBase: time.Millisecond, BackoffCap: time.Millisecond, MaxAttempts: 3, Deadline: time.Minute,
		newClient: func(b Bucket, c Credentials) *s3.Client {
			return s3.New(s3.Options{Region: b.Region, BaseEndpoint: aws.String(server.URL), UsePathStyle: true,
				Credentials: credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""), HTTPClient: server.Client()})
		}}
	bucket, err := s.Save(ctx, org, "", BucketInput{Name: "b", Endpoint: "https://s3.example.com", Region: "test",
		Bucket: "files", AccessKey: "key", SecretKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Link(ctx, org, session, &bucket.ID); err != nil {
		t.Fatal(err)
	}
	capture := func(message string) string {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"waMessageId": message, "media": domain.MediaMeta{Mimetype: "image/jpeg"},
			"_mediaSource": base64.StdEncoding.EncodeToString([]byte("descriptor"))})
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		clean, e := s.Capture(ctx, tx, store.GatewayEvent{Type: domain.EventMessage, OrganizationID: org, SessionID: session, Payload: payload})
		if e != nil {
			_ = tx.Rollback()
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		var p struct {
			Media domain.MediaMeta `json:"media"`
		}
		_ = json.Unmarshal(clean, &p)
		return p.Media.ID
	}
	return s, db, capture
}

func assetState(t *testing.T, db *sql.DB, id string) (status string, attempts int, next sql.NullInt64, lastErr sql.NullString) {
	t.Helper()
	if err := db.QueryRow(`SELECT status,attempts,next_attempt_at,last_error FROM media_assets WHERE id=?`, id).
		Scan(&status, &attempts, &next, &lastErr); err != nil {
		t.Fatal(err)
	}
	return
}

// A transfer that keeps failing stops after MaxAttempts with status failed
// and a reason, instead of retrying forever.
func TestMediaWorkerMySQLAttemptCapFailsAsset(t *testing.T) {
	downloader := &failingDownload{err: errors.New("gateway unavailable")}
	s, db, capture := mediaWorkerFixture(t, downloader)
	id := capture("msg-cap")
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := db.Exec(`UPDATE media_assets SET next_attempt_at=0 WHERE id=? AND next_attempt_at IS NOT NULL`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	status, attempts, next, lastErr := assetState(t, db, id)
	if status != "failed" || attempts != 3 || next.Valid || !lastErr.Valid || downloader.calls != 3 {
		t.Fatalf("status=%s attempts=%d next=%v err=%v calls=%d", status, attempts, next, lastErr, downloader.calls)
	}
}

// Media WhatsApp no longer serves (404/410) is terminal on the first attempt.
func TestMediaWorkerMySQLGoneMediaFailsAtOnce(t *testing.T) {
	downloader := &failingDownload{err: domain.ErrConflict(domain.MediaGoneMessage)}
	s, db, capture := mediaWorkerFixture(t, downloader)
	id := capture("msg-gone")
	if _, err := s.step(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, attempts, next, lastErr := assetState(t, db, id)
	if status != "failed" || attempts != 1 || next.Valid || lastErr.String != "Attachment is no longer available on WhatsApp servers" {
		t.Fatalf("status=%s attempts=%d next=%v err=%v", status, attempts, next, lastErr)
	}
}

// A claimed asset is leased: a second worker skips it, and the first
// worker's late outcome is refused once another claim replaced its lease.
func TestMediaWorkerMySQLClaimLease(t *testing.T) {
	s, db, capture := mediaWorkerFixture(t, &failingDownload{err: errors.New("unused")})
	id := capture("msg-lease")
	ctx := context.Background()
	a, token, err := s.claim(ctx)
	if err != nil || a.ID != id {
		t.Fatalf("claim = %s %v", a.ID, err)
	}
	if _, _, err := s.claim(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("leased asset claimed twice: %v", err)
	}
	if _, err := db.Exec(`UPDATE media_assets SET lease_until=0 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if b, _, err := s.claim(ctx); err != nil || b.ID != id {
		t.Fatalf("expired lease not reclaimed: %v", err)
	}
	if err := s.commitTransfer(ctx, &a, token); !errors.Is(err, errMediaClaimLost) {
		t.Fatalf("stale claim committed: %v", err)
	}
}
