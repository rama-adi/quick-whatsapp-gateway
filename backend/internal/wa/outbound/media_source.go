package outbound

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// resolveMedia returns the bytes for a media send, from base64 data or a URL.
func resolveMedia(ctx context.Context, m *domain.MediaPayload) ([]byte, string, error) {
	if m == nil {
		return nil, "", domain.ErrValidation("media is required")
	}
	if strings.TrimSpace(m.URL) != "" {
		return fetchMediaURL(ctx, m)
	}
	return decodeMedia(m)
}

// decodeMedia decodes the base64 payload of a media send into raw bytes and
// returns the declared mimetype. It accepts a bare base64 string or a data: URI
// (pulling the mimetype from the URI when one isn't given), tolerates padded or
// raw base64, and enforces MaxMediaBytes. Decode/size failures are validation
// errors (400), never 500s.
func decodeMedia(m *domain.MediaPayload) ([]byte, string, error) {
	if m == nil || strings.TrimSpace(m.Data) == "" {
		return nil, "", domain.ErrValidation("media.data (base64) is required when media.url is not provided")
	}
	raw := strings.TrimSpace(m.Data)
	mimetype := m.Mimetype
	if strings.HasPrefix(raw, "data:") {
		if i := strings.IndexByte(raw, ','); i >= 0 {
			header := raw[len("data:"):i] // e.g. "image/png;base64"
			raw = raw[i+1:]
			if mimetype == "" {
				if j := strings.IndexByte(header, ';'); j >= 0 {
					mimetype = header[:j]
				} else {
					mimetype = header
				}
			}
		}
	}
	data, err := decodeBase64(raw)
	if err != nil {
		return nil, "", domain.ErrValidation("media.data must be valid base64")
	}
	if len(data) == 0 {
		return nil, "", domain.ErrValidation("media.data decoded to empty")
	}
	if len(data) > MaxMediaBytes {
		return nil, "", domain.ErrValidation(fmt.Sprintf("media exceeds the %d byte limit", MaxMediaBytes))
	}
	return data, mimetype, nil
}

func fetchMediaURL(ctx context.Context, m *domain.MediaPayload) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(m.URL), nil)
	if err != nil {
		return nil, "", domain.ErrValidation("media.url must be a valid http(s) URL")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", domain.ErrValidation("media.url could not be downloaded")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", domain.ErrValidation(fmt.Sprintf("media.url returned HTTP %d", resp.StatusCode))
	}
	if resp.ContentLength > MaxMediaBytes {
		return nil, "", domain.ErrValidation(fmt.Sprintf("media exceeds the %d byte limit", MaxMediaBytes))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxMediaBytes+1))
	if err != nil {
		return nil, "", domain.ErrValidation("media.url could not be read")
	}
	if len(body) == 0 {
		return nil, "", domain.ErrValidation("media.url downloaded an empty file")
	}
	if len(body) > MaxMediaBytes {
		return nil, "", domain.ErrValidation(fmt.Sprintf("media exceeds the %d byte limit", MaxMediaBytes))
	}
	mimetype := m.Mimetype
	if mimetype == "" {
		mimetype = resp.Header.Get("Content-Type")
		if i := strings.IndexByte(mimetype, ';'); i >= 0 {
			mimetype = strings.TrimSpace(mimetype[:i])
		}
	}
	return body, mimetype, nil
}

// decodeBase64 accepts both standard (padded) and raw (unpadded) base64.
func decodeBase64(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}
