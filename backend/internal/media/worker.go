package media

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
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
}

const assetWorkColumns = `id,organization_id,session_id,message_id,bucket_id,object_key,access_token,version_id,source,status,expires_at,mimetype,filename,size,attempts,attachment_index`

var assetColumns = strings.Replace(assetWorkColumns, ",source,", ",NULL AS source,", 1)

func scanAsset(row interface{ Scan(...any) error }) (Asset, error) {
	var a Asset
	err := row.Scan(&a.ID, &a.org, &a.SessionID, &a.MessageID, &a.BucketID, &a.key, &a.token, &a.version, &a.source, &a.Status, &a.ExpiresAt, &a.Mimetype, &a.Filename, &a.Size, &a.attempts, &a.Index)
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

// errMediaClaimLost reports a fenced write whose claim token no longer owns
// the asset: its lease expired and another worker claimed it.
var errMediaClaimLost = errors.New("media asset claim lost")

// mediaLeaseMargin keeps a claim's lease longer than the step deadline that
// bounds its I/O, so a live worker never loses its lease mid-transfer.
const mediaLeaseMargin = time.Minute

// step claims one due asset, transfers it without holding any row lock, and
// records the outcome fenced by the claim token. found reports that an asset
// was claimed; err is only an infrastructure or bookkeeping failure.
func (s *Service) step(ctx context.Context) (bool, error) {
	a, token, err := s.claim(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = s.transfer(ctx, &a)
	if err == nil {
		err = s.commitTransfer(ctx, &a, token)
	}
	if err == nil || errors.Is(err, errMediaClaimLost) {
		return true, nil
	}
	if saveErr := s.recordFailure(ctx, a, token, err); saveErr != nil && !errors.Is(saveErr, errMediaClaimLost) {
		return true, saveErr
	}
	return true, nil
}

// claim leases the oldest due asset that no live claim holds. SKIP LOCKED
// keeps concurrent API replicas on different assets; the lock is released
// when the claim commits, before any I/O.
func (s *Service) claim(ctx context.Context) (Asset, string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Asset{}, "", err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	a, err := scanAsset(tx.QueryRowContext(ctx, `SELECT `+assetWorkColumns+` FROM media_assets
		WHERE next_attempt_at<=? AND (lease_until IS NULL OR lease_until<=?)
		ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, now, now))
	if err != nil {
		return Asset{}, "", err
	}
	token := domain.NewULID()
	lease := time.Now().Add(s.Deadline + mediaLeaseMargin).UnixMilli()
	if _, err = tx.ExecContext(ctx, `UPDATE media_assets SET claim_token=?,lease_until=? WHERE id=?`, token, lease, a.ID); err != nil {
		return Asset{}, "", err
	}
	return a, token, tx.Commit()
}

// recordFailure classifies a failed transfer. A policy rejection or media
// that WhatsApp no longer serves (404/410) is terminal at once; any other
// error is retried with backoff until MaxAttempts, then the asset is failed.
// A failed asset is still visited at its expiry for cleanup.
func (s *Service) recordFailure(ctx context.Context, a Asset, token string, cause error) error {
	attempts := a.attempts + 1
	reason := ""
	var apiErr *domain.APIError
	switch {
	case errors.As(cause, &apiErr) && apiErr.Code == domain.CodeValidationError:
		reason = "Attachment rejected by the gateway media policy"
	case errors.As(cause, &apiErr) && apiErr.Code == domain.CodeConflict && apiErr.Message == domain.MediaGoneMessage:
		reason = "Attachment is no longer available on WhatsApp servers"
	case s.MaxAttempts > 0 && attempts >= int64(s.MaxAttempts):
		reason = fmt.Sprintf("Attachment transfer gave up after %d attempts; check connection credentials, permissions and gateway availability", attempts)
	}
	if reason != "" {
		slog.WarnContext(ctx, "attachment failed", "asset", a.ID, "session", a.SessionID, "attempts", attempts,
			"reason", reason, "error", cause)
		// Keep failure text free of credentials and signed URLs.
		return s.fenced(ctx, `UPDATE media_assets SET status='failed',attempts=?,next_attempt_at=expires_at,last_error=?,
			claim_token=NULL,lease_until=NULL WHERE id=? AND claim_token=?`, attempts, reason, a.ID, token)
	}
	delay := s.BackoffBase
	for i := int64(0); i < a.attempts && delay < s.BackoffCap; i++ {
		if delay > s.BackoffCap/2 {
			delay = s.BackoffCap
			break
		}
		delay *= 2
	}
	return s.fenced(ctx, `UPDATE media_assets SET attempts=?,next_attempt_at=?,last_error=?,claim_token=NULL,lease_until=NULL
		WHERE id=? AND claim_token=?`, attempts, time.Now().Add(min(delay, s.BackoffCap)).UnixMilli(),
		"Attachment transfer failed; check connection credentials, permissions and gateway availability", a.ID, token)
}

func (s *Service) fenced(ctx context.Context, query string, args ...any) error {
	res, err := s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errMediaClaimLost
	}
	return nil
}

// transfer performs the asset's I/O (upload from the stored source, or
// cleanup at expiry) and updates a in memory. It holds no database lock.
func (s *Service) transfer(ctx context.Context, a *Asset) error {
	b, c, err := s.connection(ctx, s.DB, a.org, a.BucketID)
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
		return s.transfer(ctx, a)
	}
	return nil
}

// commitTransfer records the transfer outcome, its media.ready/media.expired
// event and the event's durable fan-out in one transaction, fenced by the
// claim token. A ready asset is next visited at its expiry; an expired one
// needs no further work.
func (s *Service) commitTransfer(ctx context.Context, a *Asset, token string) error {
	typ := domain.EventMediaReady
	var next *int64
	if a.Status == "expired" {
		typ = domain.EventMediaExpired
	} else {
		next = a.ExpiresAt
	}
	event := domain.NewEvent(typ, a.SessionID, a.org, *a)
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE media_assets SET status=?,source=NULL,version_id=?,size=?,next_attempt_at=?,
		attempts=0,last_error=NULL,claim_token=NULL,lease_until=NULL WHERE id=? AND claim_token=?`,
		a.Status, a.version, a.Size, next, a.ID, token)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errMediaClaimLost
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO event_log(event_id,organization_id,session_id,type,payload,created_at) VALUES(?,?,?,?,?,?)`,
		event.ID, event.Organization, event.Session, event.Type, payload, event.Timestamp); err != nil {
		return err
	}
	if err = store.EnqueueAPIFanout(ctx, tx, event.ID, event.Organization, event.Session, event.Timestamp); err != nil {
		return err
	}
	return tx.Commit()
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
