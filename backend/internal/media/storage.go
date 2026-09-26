// Package media owns organization S3 connections and durable attachment storage.
package media

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/crypto"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

type Bucket struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	Bucket        string `json:"bucket"`
	PathStyle     bool   `json:"pathStyle"`
	RetentionDays *int64 `json:"retentionDays" doc:"Retention in days, at least 1. Null keeps attachments indefinitely. Changes apply to new attachments."`
}
type BucketInput struct {
	Name          string `json:"name"`
	Endpoint      string `json:"endpoint" doc:"S3 service endpoint. HTTPS is required except for trusted internal domains."`
	Region        string `json:"region"`
	Bucket        string `json:"bucket"`
	PathStyle     bool   `json:"pathStyle"`
	RetentionDays *int64 `json:"retentionDays" minimum:"1"`
	AccessKey     string `json:"accessKey"`
	SecretKey     string `json:"secretKey"`
}
type Credentials struct{ AccessKey, SecretKey string }
type Downloader interface {
	DownloadMedia(context.Context, string, string, []byte) ([]byte, error)
}
type Service struct {
	// InternalDomains permits HTTP and private IPs for operator-trusted DNS names.
	InternalDomains []string
	// Transport optionally supplies the external S3 network boundary. Nil uses the validated transport.
	Transport                               http.RoundTripper
	newClient                               func(Bucket, Credentials) *s3.Client
	DB                                      *sql.DB
	Cipher                                  *crypto.AESGCM
	BaseURL                                 string
	Downloader                              Downloader
	Poll, Deadline, BackoffBase, BackoffCap time.Duration
	Publish                                 func(context.Context, domain.Event) error
}

func validate(in BucketInput, domains ...string) error {
	u, err := url.Parse(in.Endpoint)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && internalHost(u.Hostname(), domains))) || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return domain.ErrValidation("endpoint must use HTTPS, or HTTP on a trusted internal domain")
	}
	if strings.TrimSpace(in.Name) == "" || in.Bucket == "" || in.Region == "" {
		return domain.ErrValidation("name, bucket and region are required")
	}
	if strings.ContainsAny(in.Bucket, "/\\") {
		return domain.ErrValidation("bucket must be a bucket name")
	}
	if in.RetentionDays != nil && (*in.RetentionDays < 1 || *in.RetentionDays > (1<<63-1-time.Now().UnixMilli())/86400000) {
		return domain.ErrValidation("retentionDays must be at least 1 and fit an epoch-millisecond expiry")
	}
	return nil
}
func (s *Service) List(ctx context.Context, org string) ([]Bucket, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,endpoint,region,bucket,path_style,retention_days FROM media_buckets WHERE organization_id=? ORDER BY created_at,id`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bucket{}
	for rows.Next() {
		var b Bucket
		if err = rows.Scan(&b.ID, &b.Name, &b.Endpoint, &b.Region, &b.Bucket, &b.PathStyle, &b.RetentionDays); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func (s *Service) Save(ctx context.Context, org, id string, in BucketInput) (Bucket, error) {
	if err := validate(in, s.InternalDomains...); err != nil {
		return Bucket{}, err
	}
	if id != "" {
		old, _, err := s.connection(ctx, s.DB, org, id)
		if err != nil {
			return Bucket{}, err
		}
		if old.Endpoint != in.Endpoint || old.Region != in.Region || old.Bucket != in.Bucket || old.PathStyle != in.PathStyle {
			return Bucket{}, domain.ErrValidation("create a new connection to change the storage destination; existing attachments retain their original connection")
		}
	}
	var encrypted []byte
	if in.AccessKey != "" || in.SecretKey != "" {
		if in.AccessKey == "" || in.SecretKey == "" {
			return Bucket{}, domain.ErrValidation("provide both accessKey and secretKey")
		}
		raw, _ := json.Marshal(Credentials{AccessKey: in.AccessKey, SecretKey: in.SecretKey})
		var err error
		encrypted, err = s.Cipher.Encrypt(raw)
		if err != nil {
			return Bucket{}, err
		}
	}
	if id == "" {
		if len(encrypted) == 0 {
			return Bucket{}, domain.ErrValidation("accessKey and secretKey are required")
		}
		id = domain.NewULID()
		_, err := s.DB.ExecContext(ctx, `INSERT INTO media_buckets (id,organization_id,name,endpoint,region,bucket,path_style,credentials,retention_days,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, id, org, in.Name, in.Endpoint, in.Region, in.Bucket, in.PathStyle, encrypted, in.RetentionDays, time.Now().UnixMilli())
		if err != nil {
			return Bucket{}, err
		}
	} else {
		_, err := s.DB.ExecContext(ctx, `UPDATE media_buckets SET name=?,retention_days=?,credentials=COALESCE(?,credentials) WHERE id=? AND organization_id=?`, in.Name, in.RetentionDays, encrypted, id, org)
		if err != nil {
			return Bucket{}, err
		}
	}
	return Bucket{ID: id, Name: in.Name, Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, PathStyle: in.PathStyle, RetentionDays: in.RetentionDays}, nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Service) connection(ctx context.Context, q queryer, org, id string) (Bucket, Credentials, error) {
	var b Bucket
	var encrypted []byte
	err := q.QueryRowContext(ctx, `SELECT id,name,endpoint,region,bucket,path_style,retention_days,credentials FROM media_buckets WHERE id=? AND organization_id=?`, id, org).Scan(&b.ID, &b.Name, &b.Endpoint, &b.Region, &b.Bucket, &b.PathStyle, &b.RetentionDays, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return b, Credentials{}, domain.ErrNotFound("storage connection")
	}
	if err != nil {
		return b, Credentials{}, err
	}
	raw, err := s.Cipher.Decrypt(encrypted)
	if err != nil {
		return b, Credentials{}, err
	}
	var c Credentials
	err = json.Unmarshal(raw, &c)
	return b, c, err
}
func (s *Service) Binding(ctx context.Context, org, session string) (*string, error) {
	var id *string
	err := s.DB.QueryRowContext(ctx, `SELECT (SELECT bucket_id FROM session_media_storage WHERE session_id=? AND organization_id=?) FROM wa_sessions WHERE id=? AND organization_id=?`, session, org, session, org).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound("session")
	}
	return id, err
}
func (s *Service) Link(ctx context.Context, org, session string, id *string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sid string
	err = tx.QueryRowContext(ctx, `SELECT id FROM wa_sessions WHERE id=? AND organization_id=? FOR UPDATE`, session, org).Scan(&sid)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound("session")
	}
	if err != nil {
		return err
	}
	if id == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM session_media_storage WHERE session_id=? AND organization_id=?`, session, org)
	} else {
		var bid string
		err = tx.QueryRowContext(ctx, `SELECT id FROM media_buckets WHERE id=? AND organization_id=? LOCK IN SHARE MODE`, *id, org).Scan(&bid)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound("storage connection")
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO session_media_storage(session_id,organization_id,bucket_id) VALUES(?,?,?) ON DUPLICATE KEY UPDATE bucket_id=VALUES(bucket_id)`, session, org, *id)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) Delete(ctx context.Context, org, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id FROM media_buckets WHERE id=? AND organization_id=? FOR UPDATE`, id, org).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound("storage connection")
	}
	if err != nil {
		return err
	}
	var active int
	err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM session_media_storage WHERE bucket_id=?) + (SELECT COUNT(*) FROM media_assets WHERE bucket_id=? AND (status<>'expired' OR notification IS NOT NULL))`, id, id).Scan(&active)
	if err != nil {
		return err
	}
	if active != 0 {
		return domain.ErrValidation("unlink sessions and finish attachment cleanup before deleting the connection")
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM media_assets WHERE bucket_id=? AND organization_id=?`, id, org); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM media_buckets WHERE id=? AND organization_id=?`, id, org); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) s3Client(b Bucket, c Credentials) *s3.Client {
	if s.newClient != nil {
		return s.newClient(b, c)
	}
	cl := client(b, c, s.InternalDomains)
	if s.Transport != nil {
		cl = s3.New(s3.Options{Region: b.Region, BaseEndpoint: &b.Endpoint, UsePathStyle: b.PathStyle, Credentials: credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""), HTTPClient: &http.Client{Transport: s.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	}
	return cl
}
