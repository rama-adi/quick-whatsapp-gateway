package media

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

type Asset struct {
	Index           int    `json:"index" doc:"Zero-based position within an album; zero for a single attachment."`
	ID              string `json:"id"`
	SessionID       string `json:"sessionId"`
	MessageID       string `json:"waMessageId"`
	BucketID        string `json:"bucketId"`
	Status          string `json:"status"`
	URL             string `json:"url,omitempty"`
	ExpiresAt       *int64 `json:"expiresAt,omitempty"`
	Mimetype        string `json:"mimetype"`
	Filename        string `json:"filename"`
	Size            int64  `json:"size"`
	org, key, token string
	version         *string
	source          []byte
	attempts        int64
	notification    []byte
}

const assetWorkColumns = `id,organization_id,session_id,message_id,bucket_id,object_key,access_token,version_id,source,status,expires_at,mimetype,filename,size,attempts,notification,attachment_index`

var assetColumns = strings.Replace(assetWorkColumns, ",source,", ",NULL AS source,", 1)

func scanAsset(row interface{ Scan(...any) error }) (Asset, error) {
	var a Asset
	err := row.Scan(&a.ID, &a.org, &a.SessionID, &a.MessageID, &a.BucketID, &a.key, &a.token, &a.version, &a.source, &a.Status, &a.ExpiresAt, &a.Mimetype, &a.Filename, &a.Size, &a.attempts, &a.notification, &a.Index)
	return a, err
}
func (s *Service) assetURL(a Asset) string {
	return strings.TrimRight(s.BaseURL, "/") + "/api/v1/media/" + a.ID + "/content?token=" + a.token
}
func expired(a Asset, now int64) bool { return a.ExpiresAt != nil && *a.ExpiresAt <= now }
func (s *Service) Run(ctx context.Context) error {
	if s.Poll <= 0 || s.Deadline <= 0 || s.BackoffBase <= 0 || s.BackoffCap < s.BackoffBase {
		return errors.New("media worker timing configuration required")
	}
	for ctx.Err() == nil {
		workCtx, cancel := context.WithTimeout(ctx, s.Deadline)
		found, err := s.step(workCtx)
		cancel()
		if err != nil {
			slog.ErrorContext(ctx, "attachment worker failed", "error", err)
		}
		if found && err == nil {
			continue
		}
		timer := time.NewTimer(s.Poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

// Row locks remain held for one bounded I/O operation. SKIP LOCKED lets other
// API replicas process different assets without overlapping uploads/deletes.
func (s *Service) step(ctx context.Context) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	a, err := scanAsset(tx.QueryRowContext(ctx, `SELECT `+assetWorkColumns+` FROM media_assets WHERE next_attempt_at<=? ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, now))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if a.notification != nil && (a.Status == "expired" || !expired(a, now)) {
		var event domain.Event
		err = json.Unmarshal(a.notification, &event)
		if err == nil && s.Publish != nil {
			err = s.Publish(ctx, event)
		}
		if err == nil {
			next := int64(1<<63 - 1)
			if a.Status == "ready" && a.ExpiresAt != nil {
				next = *a.ExpiresAt
			}
			_, err = tx.ExecContext(ctx, `UPDATE media_assets SET notification=NULL,next_attempt_at=? WHERE id=?`, next, a.ID)
		}
	} else {
		err = s.process(ctx, tx, &a)
	}
	if err != nil {
		var failure *domain.APIError
		if errors.As(err, &failure) && failure.Code == domain.CodeValidationError {
			next := int64(1<<63 - 1)
			if a.ExpiresAt != nil {
				next = *a.ExpiresAt
			}
			if _, saveErr := tx.ExecContext(ctx, `UPDATE media_assets SET status='failed',next_attempt_at=?,last_error=? WHERE id=?`, next, "Attachment rejected by the gateway media policy", a.ID); saveErr != nil {
				return true, saveErr
			}
			return true, tx.Commit()
		}
		// Keep failure text free of credentials and signed URLs.
		delay := s.BackoffBase
		for i := int64(0); i < a.attempts && delay < s.BackoffCap; i++ {
			if delay > s.BackoffCap/2 {
				delay = s.BackoffCap
				break
			}
			delay *= 2
		}
		_, saveErr := tx.ExecContext(ctx, `UPDATE media_assets SET attempts=attempts+1,next_attempt_at=?,last_error=? WHERE id=?`, time.Now().Add(min(delay, s.BackoffCap)).UnixMilli(), "Attachment transfer failed; check connection credentials, permissions and gateway availability", a.ID)
		if saveErr != nil {
			return true, saveErr
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return true, commitErr
	}
	return true, nil
}
func (s *Service) process(ctx context.Context, tx *sql.Tx, a *Asset) error {
	b, c, err := s.connection(ctx, tx, a.org, a.BucketID)
	if err != nil {
		return err
	}
	cl := s.s3Client(b, c)
	if expired(*a, time.Now().UnixMilli()) {
		// HEAD recovers an upload whose successful response was lost before commit.
		exists := true
		if a.version == nil {
			head, e := cl.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(b.Bucket), Key: aws.String(a.key)})
			if e != nil && !missingObject(e) {
				return e
			}
			if e == nil {
				a.version = head.VersionId
			} else {
				exists = false
			}
		}
		if exists {
			_, err = cl.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.Bucket), Key: aws.String(a.key), VersionId: a.version})
			if err != nil {
				return err
			}
		}
		a.Status = "expired"
		a.URL = ""
		a.source = nil
	} else {
		head, e := cl.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(b.Bucket), Key: aws.String(a.key)})
		if e == nil {
			a.version = head.VersionId
			a.Size = aws.ToInt64(head.ContentLength)
		} else {
			if !missingObject(e) {
				return e
			}
			source, e := s.Cipher.Decrypt(a.source)
			if e != nil {
				return e
			}
			if s.Downloader == nil {
				return errors.New("media downloader unavailable")
			}
			var inline struct {
				Data string `json:"data"`
			}
			var data []byte
			if json.Unmarshal(source, &inline) == nil && inline.Data != "" {
				data, e = base64.StdEncoding.DecodeString(inline.Data)
			} else {
				data, e = s.Downloader.DownloadMedia(ctx, a.org, a.SessionID, source)
			}
			if e != nil {
				return e
			}
			result, e := cl.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.Bucket), Key: aws.String(a.key), Body: bytes.NewReader(data), ContentType: aws.String(a.Mimetype), IfNoneMatch: aws.String("*")})
			if e != nil {
				return e
			}
			a.version = result.VersionId
			a.Size = int64(len(data))
		}
		a.Status = "ready"
		a.URL = s.assetURL(*a)
		a.source = nil
	}
	if a.Status == "ready" && expired(*a, time.Now().UnixMilli()) {
		return s.process(ctx, tx, a)
	}
	typ := domain.EventMediaReady
	if a.Status == "expired" {
		typ = domain.EventMediaExpired
	}
	event := domain.NewEvent(typ, a.SessionID, a.org, *a)
	notification, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE media_assets SET status=?,source=NULL,version_id=?,size=?,notification=?,next_attempt_at=?,attempts=0,last_error=NULL WHERE id=?`, a.Status, a.version, a.Size, notification, time.Now().UnixMilli(), a.ID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO event_log(event_id,organization_id,session_id,type,payload,created_at) VALUES(?,?,?,?,?,?)`, event.ID, event.Organization, event.Session, event.Type, payload, event.Timestamp)
	return err
}
func missingObject(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && (api.ErrorCode() == "NotFound" || api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "404")
}
func (s *Service) Get(ctx context.Context, org, id string) (Asset, error) {
	a, err := scanAsset(s.DB.QueryRowContext(ctx, `SELECT `+assetColumns+` FROM media_assets WHERE id=? AND organization_id=?`, id, org))
	if errors.Is(err, sql.ErrNoRows) {
		return a, domain.ErrNotFound("attachment")
	}
	if err != nil {
		return a, err
	}
	if expired(a, time.Now().UnixMilli()) {
		a.Status = "expired"
	} else if a.Status == "ready" {
		a.URL = s.assetURL(a)
	}
	return a, nil
}
func (s *Service) Enrich(ctx context.Context, org string, messages []domain.Message) error {
	if len(messages) == 0 {
		return nil
	}
	for i := range messages {
		if messages[i].MediaMeta != nil {
			messages[i].MediaMeta.URL = ""
			messages[i].MediaMeta.Items = nil
		}
	}
	ids := make([]any, 0, len(messages)+2)
	ids = append(ids, org, messages[0].SessionID)
	marks := make([]string, 0, len(messages))
	for _, m := range messages {
		ids = append(ids, m.WAMessageID)
		marks = append(marks, "?")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+assetColumns+` FROM media_assets WHERE organization_id=? AND session_id=? AND message_id IN (`+strings.Join(marks, ",")+`)`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	assets := map[string][]Asset{}
	for rows.Next() {
		a, e := scanAsset(rows)
		if e != nil {
			return e
		}
		assets[a.MessageID] = append(assets[a.MessageID], a)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range messages {
		attachments, ok := assets[messages[i].WAMessageID]
		if !ok {
			continue
		}
		sort.Slice(attachments, func(i, j int) bool { return attachments[i].Index < attachments[j].Index })
		items := make([]domain.MediaMeta, 0, len(attachments))
		for _, a := range attachments {
			m := domain.MediaMeta{ID: a.ID, ExpiresAt: a.ExpiresAt, Mimetype: a.Mimetype, Filename: a.Filename, Size: a.Size}
			if a.Status == "ready" && !expired(a, time.Now().UnixMilli()) {
				m.URL = s.assetURL(a)
			}
			items = append(items, m)
		}
		m := items[0]
		if len(items) > 1 {
			m.Items = items
		}
		messages[i].MediaMeta = &m
	}
	return nil
}
