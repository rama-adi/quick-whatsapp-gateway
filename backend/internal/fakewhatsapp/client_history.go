package fakewhatsapp

import (
	"context"
	"encoding/json"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/historysync"
)

var _ historysync.Client = (*Client)(nil)

// DownloadHistorySync fetches a downloadable blob from the fake media server
// (an inline bootstrap payload needs no fetch), then lets the local whatsmeow
// client inflate, decode and store it exactly like a real download.
func (c *Client) DownloadHistorySync(ctx context.Context, notif *waE2E.HistorySyncNotification,
	synchronousStorage bool) (*waHistorySync.HistorySync, error) {
	if len(notif.GetInitialHistBootstrapInlinePayload()) == 0 {
		if notif.GetDirectPath() == "" {
			return nil, fmt.Errorf("failed to download: %w", whatsmeow.ErrNoURLPresent)
		}
		response, err := c.operation(ctx, OperationRequest{Kind: "history_download", To: notif.GetDirectPath()})
		if response.Status == historyDownloadNotFound {
			return nil, fmt.Errorf("failed to download: %w", whatsmeow.ErrMediaDownloadFailedWith404)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to download: %w", err)
		}
		var data []byte
		if err := json.Unmarshal(response.Result, &data); err != nil {
			return nil, fmt.Errorf("failed to download: %w", err)
		}
		inline := proto.Clone(notif).(*waE2E.HistorySyncNotification)
		inline.InitialHistBootstrapInlinePayload = data
		notif = inline
	}
	return c.local.DownloadHistorySync(ctx, notif, synchronousStorage)
}

// ParseWebMessage uses the local whatsmeow parser with the paired identity.
func (c *Client) ParseWebMessage(chat types.JID, msg *waWeb.WebMessageInfo) (*events.Message, error) {
	return c.local.ParseWebMessage(chat, msg)
}

// SendProtocolMessageReceipt records the receipt at the fake peer.
func (c *Client) SendProtocolMessageReceipt(ctx context.Context, id types.MessageID, msgType types.ReceiptType) error {
	if msgType != types.ReceiptTypeHistorySync {
		return nil
	}
	_, err := c.operation(ctx, OperationRequest{Kind: "history_receipt", To: string(id)})
	return err
}

// SendHistorySyncServerErrorReceipt records the re-upload request.
func (c *Client) SendHistorySyncServerErrorReceipt(ctx context.Context, id types.MessageID, _ []byte) error {
	_, err := c.operation(ctx, OperationRequest{Kind: "history_server_error", To: string(id)})
	return err
}

// DeleteMedia removes a history blob from the fake media server.
func (c *Client) DeleteMedia(ctx context.Context, _ whatsmeow.MediaType, directPath string, _ []byte, _ string) error {
	_, err := c.operation(ctx, OperationRequest{Kind: "delete_media", To: directPath})
	return err
}
