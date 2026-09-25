package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
)

// StickerRepo stores content globally. Message references carry no copied bytes.
type StickerRepo struct{ db storedb.DBTX }

func NewStickerRepo(db storedb.DBTX) *StickerRepo { return &StickerRepo{db: db} }

func (r *StickerRepo) Put(ctx context.Context, content []byte) (string, error) {
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	_, err := r.db.ExecContext(ctx, `INSERT INTO sticker_blobs(sha256,content) VALUES (?,?) ON DUPLICATE KEY UPDATE sha256=VALUES(sha256)`, hash, content)
	return hash, err
}
func (r *StickerRepo) Has(ctx context.Context, hash string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sticker_blobs WHERE sha256=?)`, hash).Scan(&exists)
	return exists, err
}
func (r *StickerRepo) Link(ctx context.Context, session, chat, message, hash string) error {
	if message == "" || chat == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO sticker_messages(session_id,chat_jid,message_id,sha256) VALUES (?,?,?,?) ON DUPLICATE KEY UPDATE sha256=VALUES(sha256)`, session, chat, message, hash)
	return err
}
func (r *StickerRepo) Reference(ctx context.Context, session, chat, message string) (*apitypes.StickerData, error) {
	if message == "" {
		return nil, nil
	}
	var hash string
	err := r.db.QueryRowContext(ctx, `SELECT sha256 FROM sticker_messages WHERE session_id=? AND chat_jid=? AND message_id=?`, session, chat, message).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &apitypes.StickerData{SHA256: hash, Status: "available", Mimetype: "image/webp"}, nil
}

// ExpandPayload is used only at public delivery/read boundaries, never persistence.
func (r *StickerRepo) ExpandPayload(ctx context.Context, raw []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw, nil
	}
	changed := false
	for _, field := range []string{"sticker", "quotedSticker"} {
		value, ok := payload[field]
		if !ok {
			continue
		}
		var sticker apitypes.StickerData
		if err := json.Unmarshal(value, &sticker); err != nil {
			return nil, err
		}
		if sticker.Status != "available" || sticker.SHA256 == "" {
			continue
		}
		var content []byte
		if err := r.db.QueryRowContext(ctx, `SELECT content FROM sticker_blobs WHERE sha256=?`, sticker.SHA256).Scan(&content); err != nil {
			return nil, err
		}
		sticker.Base64 = base64.StdEncoding.EncodeToString(content)
		encoded, err := json.Marshal(sticker)
		if err != nil {
			return nil, err
		}
		payload[field] = encoded
		changed = true
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(payload)
}
