package outbound

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// PrepareEngineMedia resolves public media inputs into the byte-only engine
// contract without changing the caller's durable payload. Existing media limits
// and download deadlines apply before the private RPC is issued.
func PrepareEngineMedia(ctx context.Context, req domain.SendRequest) (domain.SendRequest, error) {
	switch req.Type {
	case domain.SendTypeImage, domain.SendTypeVideo, domain.SendTypeAudio, domain.SendTypeDocument, domain.SendTypeSticker:
		if err := Validate(req); err != nil {
			return domain.SendRequest{}, err
		}
		data, mimetype, err := resolveMedia(ctx, req.Media)
		if err != nil {
			return domain.SendRequest{}, err
		}
		media := *req.Media
		media.Data = base64.StdEncoding.EncodeToString(data)
		media.URL = ""
		media.Mimetype = mimetype
		req.Media = &media
	case domain.SendTypeAlbum:
		if err := Validate(req); err != nil {
			return domain.SendRequest{}, err
		}
		req.Medias = slices.Clone(req.Medias)
		total := 0
		for i, item := range req.Medias {
			media := &domain.MediaPayload{Data: item.Data, URL: item.URL, Mimetype: item.Mimetype}
			data, mimetype, err := resolveMedia(ctx, media)
			if err != nil {
				return domain.SendRequest{}, fmt.Errorf("medias[%d]: %w", i, err)
			}
			total += len(data)
			if total > MaxAlbumBytes {
				return domain.SendRequest{}, domain.ErrValidation(fmt.Sprintf("album exceeds the %d byte aggregate limit", MaxAlbumBytes))
			}
			req.Medias[i].Data = base64.StdEncoding.EncodeToString(data)
			req.Medias[i].URL = ""
			req.Medias[i].Mimetype = mimetype
		}
	}
	return req, nil
}
