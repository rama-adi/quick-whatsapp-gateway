package media

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"io"
	"time"
)

func (s *Service) Open(ctx context.Context, id, token string) (io.ReadCloser, Asset, error) {
	a, err := scanAsset(s.DB.QueryRowContext(ctx, `SELECT `+assetColumns+` FROM media_assets WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, a, domain.ErrNotFound("attachment")
	}
	if err != nil {
		return nil, a, err
	}
	allowed := len(token) == 64 && subtle.ConstantTimeCompare([]byte(token), []byte(a.token)) == 1
	if !allowed || a.Status != "ready" || expired(a, time.Now().UnixMilli()) {
		return nil, a, domain.ErrNotFound("attachment")
	}
	b, c, err := s.connection(ctx, s.DB, a.org, a.BucketID)
	if err != nil {
		return nil, a, err
	}
	deadline := time.Now().Add(s.Deadline)
	if s.Deadline <= 0 {
		deadline = time.Now().Add(s.BackoffCap)
	}
	if a.ExpiresAt != nil && time.UnixMilli(*a.ExpiresAt).Before(deadline) {
		deadline = time.UnixMilli(*a.ExpiresAt)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	cl := s.s3Client(b, c)
	out, err := cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.Bucket), Key: aws.String(a.key), VersionId: a.version})
	if err != nil {
		cancel()
		return nil, a, domain.ErrNotFound("attachment content unavailable")
	}
	return &contentReader{ReadCloser: out.Body, cancel: cancel}, a, nil
}

type contentReader struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *contentReader) Close() error { err := r.ReadCloser.Close(); r.cancel(); return err }
