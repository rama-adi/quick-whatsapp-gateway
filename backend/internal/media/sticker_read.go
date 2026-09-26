package media

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// ReadSticker binds global deduplicated bytes to an owned session and exact chat/message.
func (s *Service) ReadSticker(ctx context.Context, org, session, chat, message string) (apitypes.StickerData, error) {
	var hash string
	var content []byte
	err := s.DB.QueryRowContext(ctx, `SELECT r.sha256,b.content FROM sticker_messages r JOIN sticker_blobs b ON b.sha256=r.sha256 JOIN wa_sessions s ON s.id=r.session_id WHERE s.organization_id=? AND r.session_id=? AND BINARY r.chat_jid=BINARY ? AND BINARY r.message_id=BINARY ?`, org, session, chat, message).Scan(&hash, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return apitypes.StickerData{}, domain.ErrNotFound("sticker")
	}
	if err != nil {
		return apitypes.StickerData{}, err
	}
	return apitypes.StickerData{SHA256: hash, Mimetype: "image/webp", Status: "available", Base64: base64.StdEncoding.EncodeToString(content)}, nil
}
