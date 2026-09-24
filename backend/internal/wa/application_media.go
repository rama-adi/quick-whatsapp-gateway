package wa

import (
	"context"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func (a *ApplicationGatewayAdapter) DownloadMedia(ctx context.Context, q application.SessionStateQuery, source []byte) ([]byte, error) {
	if _, err := a.GetSessionState(ctx, q); err != nil {
		return nil, err
	}
	live, ok := a.live.(*LiveOps)
	if !ok {
		return nil, domain.ErrNotImplemented("media downloader")
	}
	var msg waE2E.Message
	if err := proto.Unmarshal(source, &msg); err != nil {
		return nil, domain.ErrValidation("invalid media descriptor")
	}
	var size uint64
	switch {
	case msg.ImageMessage != nil:
		size = msg.ImageMessage.GetFileLength()
	case msg.VideoMessage != nil:
		size = msg.VideoMessage.GetFileLength()
	case msg.AudioMessage != nil:
		size = msg.AudioMessage.GetFileLength()
	case msg.DocumentMessage != nil:
		size = msg.DocumentMessage.GetFileLength()
	case msg.StickerMessage != nil:
		size = msg.StickerMessage.GetFileLength()
	default:
		return nil, domain.ErrValidation("unsupported media descriptor")
	}
	// Reuse the gateway's existing per-attachment memory policy.
	if size > outbound.MaxMediaBytes {
		return nil, domain.ErrValidation("attachment exceeds the gateway media size limit")
	}
	session := live.m.Get(q.SessionID)
	if session == nil {
		return nil, domain.ErrNotFound("session")
	}
	session.mu.Lock()
	client, ok := session.client.(interface {
		DownloadAny(context.Context, *waE2E.Message) ([]byte, error)
	})
	session.mu.Unlock()
	if !ok {
		return nil, domain.ErrNotFound("session")
	}
	data, err := client.DownloadAny(ctx, &msg)
	if err != nil {
		return nil, err
	}
	if len(data) > outbound.MaxMediaBytes {
		return nil, domain.ErrValidation("attachment exceeds the gateway media size limit")
	}
	return data, nil
}
