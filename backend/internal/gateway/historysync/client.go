package historysync

import (
	"context"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	waevents "go.mau.fi/whatsmeow/types/events"
)

// Client is the slice of the WhatsApp client the history pipeline uses. The
// real *whatsmeow.Client and the development fake both satisfy it.
type Client interface {
	// DownloadHistorySync downloads (or inflates an inline payload), decodes
	// the blob and, with synchronousStorage, stores its message secrets,
	// LID mappings and push names in the device store before returning.
	DownloadHistorySync(ctx context.Context, notif *waE2E.HistorySyncNotification,
		synchronousStorage bool) (*waHistorySync.HistorySync, error)
	ParseWebMessage(chat types.JID, msg *waWeb.WebMessageInfo) (*waevents.Message, error)
	SendProtocolMessageReceipt(ctx context.Context, id types.MessageID, msgType types.ReceiptType) error
	SendHistorySyncServerErrorReceipt(ctx context.Context, msgID types.MessageID, mediaKey []byte) error
	DeleteMedia(ctx context.Context, appInfo whatsmeow.MediaType, directPath string, encFileHash []byte,
		encHandle string) error
}

var _ Client = (*whatsmeow.Client)(nil)
