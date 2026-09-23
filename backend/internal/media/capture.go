package media

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
)

// Capture shares the ingest transaction: acknowledgement follows both the public
// event and durable upload intent. Replays cannot allocate another object key.
func (s *Service) Capture(ctx context.Context, tx storedb.DBTX, e store.GatewayEvent) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return nil, err
	}
	raw, ok := payload["_mediaSource"]
	if !ok {
		return e.Payload, nil
	}
	delete(payload, "_mediaSource")
	var index int
	_ = json.Unmarshal(payload["_mediaIndex"], &index)
	delete(payload, "_mediaIndex")
	clean, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if e.Type != domain.EventMessage && e.Type != domain.EventMessageFromMe {
		return clean, nil
	}
	var encoded string
	if err = json.Unmarshal(raw, &encoded); err != nil {
		return nil, err
	}
	source, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	var messageID string
	_ = json.Unmarshal(payload["waMessageId"], &messageID)
	if messageID == "" {
		return clean, nil
	}
	var bucket string
	var days *int64
	err = tx.QueryRowContext(ctx, `SELECT b.id,b.retention_days FROM session_media_storage m JOIN media_buckets b ON b.id=m.bucket_id AND b.organization_id=m.organization_id WHERE m.session_id=? AND m.organization_id=? LOCK IN SHARE MODE`, e.SessionID, e.OrganizationID).Scan(&bucket, &days)
	if errors.Is(err, sql.ErrNoRows) {
		return clean, nil
	}
	if err != nil {
		return nil, err
	}
	var meta domain.MediaMeta
	_ = json.Unmarshal(payload["media"], &meta)
	encrypted, err := s.Cipher.Encrypt(source)
	if err != nil {
		return nil, err
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		return nil, err
	}
	id := domain.NewULID()
	now := time.Now().UnixMilli()
	var expires *int64
	if days != nil {
		v := now + *days*86400000
		expires = &v
	}
	_, err = tx.ExecContext(ctx, `INSERT IGNORE INTO media_assets(id,organization_id,session_id,message_id,bucket_id,object_key,access_token,source,mimetype,filename,size,created_at,expires_at,next_attempt_at,attachment_index) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, e.OrganizationID, e.SessionID, messageID, bucket, "whatsapp-gateway/"+bucket+"/"+id, hex.EncodeToString(token), encrypted, meta.Mimetype, meta.Filename, meta.Size, now, expires, now, index)
	if err != nil {
		return nil, err
	}
	a, err := scanAsset(tx.QueryRowContext(ctx, `SELECT `+assetColumns+` FROM media_assets WHERE organization_id=? AND session_id=? AND message_id=? AND attachment_index=?`, e.OrganizationID, e.SessionID, messageID, index))
	if err != nil {
		return nil, err
	}
	meta.ID = a.ID
	meta.ExpiresAt = a.ExpiresAt
	if a.Status == "ready" && !expired(a, time.Now().UnixMilli()) {
		meta.URL = s.assetURL(a)
	}
	payload["media"], err = json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	return json.Marshal(payload)
}

// CaptureOutbound stores the resolved bytes supplied to the engine. The same
// unique message key reconciles WhatsApp echoes with API-originated sends.
func (s *Service) CaptureOutbound(ctx context.Context, org, session, message string, req domain.SendRequest) error {
	items := []domain.MediaPayload{}
	if req.Media != nil {
		items = append(items, *req.Media)
	}
	for _, item := range req.Medias {
		items = append(items, domain.MediaPayload{Data: item.Data, Mimetype: item.Mimetype})
	}
	if len(items) == 0 {
		return nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for index, item := range items {
		if item.Data == "" {
			continue
		}
		source, err := json.Marshal(struct {
			Data string `json:"data"`
		}{Data: item.Data})
		if err != nil {
			return err
		}
		meta := domain.MediaMeta{Mimetype: item.Mimetype, Filename: item.Filename}
		payload, err := json.Marshal(map[string]any{"waMessageId": message, "media": meta, "_mediaSource": base64.StdEncoding.EncodeToString(source), "_mediaIndex": index})
		if err != nil {
			return err
		}
		_, err = s.Capture(ctx, tx, store.GatewayEvent{Type: domain.EventMessageFromMe, OrganizationID: org, SessionID: session, Payload: payload})
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
