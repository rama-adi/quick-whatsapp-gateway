package wa

import (
	"context"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWa6"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/historysync"
)

// HistoryInbox receives WhatsApp history-sync notifications. The manager
// takes them off the live event stream before the inbound pipeline: history
// never auto-reads, never reaches the OAuth/admin interceptors and never
// becomes a `message` event. Satisfied by *historysync.Worker.
type HistoryInbox interface {
	// Accept stores the notification durably, then acknowledges it to the
	// phone through c.
	Accept(ctx context.Context, c historysync.Client, n historysync.Intercepted) error
	// Paired drops pending history of accounts other than accountJID.
	Paired(ctx context.Context, sessionID, accountJID string)
	// Discard drops a session's pending history.
	Discard(ctx context.Context, sessionID, reason string)
}

// SetHistoryInbox wires history import. It must be called before Boot: with
// an inbox, real clients are built in manual history mode (the gateway, not
// whatsmeow, downloads and acknowledges history notifications).
func (m *Manager) SetHistoryInbox(h HistoryInbox) {
	if h == nil {
		m.history.Store(nil)
		return
	}
	m.history.Store(&h)
}

// historyInbox is lock-free: it is read while a session's mutex is held.
func (m *Manager) historyInbox() HistoryInbox {
	if h := m.history.Load(); h != nil {
		return *h
	}
	return nil
}

// buildClient is the single client construction point (QR, pairing code and
// reconnect all use it), so every real client gets the same history setup.
func (m *Manager) buildClient(device *store.Device) waClient {
	c := m.newClient(device)
	if cli, ok := c.(*whatsmeow.Client); ok && m.historyInbox() != nil {
		configureHistoryClient(cli, device, m.cfg)
	}
	return c
}

// configureHistoryClient switches whatsmeow to manual history download with
// the receipt left to the gateway (sent only after the durable inbox insert),
// and applies the operator's full-sync opt-in to registration.
func configureHistoryClient(cli *whatsmeow.Client, device *store.Device, cfg Config) {
	cli.ManualHistorySyncDownload = true
	cli.DisableManualHistorySyncReceipt = true
	if cfg.HistoryFullSync {
		maxMB := cfg.HistoryFullSyncMaxMB
		cli.GetClientPayload = func() *waWa6.ClientPayload { return fullSyncClientPayload(device, maxMB) }
	}
}

// fullSyncClientPayload asks the phone for full history at registration. It
// patches a clone of whatsmeow's process-global DeviceProps (never the global
// itself, which every other client shares) and only for a device that is not
// paired yet: DeviceProps are sent only when registering.
func fullSyncClientPayload(device *store.Device, maxMB uint32) *waWa6.ClientPayload {
	payload := device.GetClientPayload()
	if device.ID != nil || payload.GetDevicePairingData() == nil {
		return payload
	}
	props := proto.Clone(store.DeviceProps).(*waCompanionReg.DeviceProps)
	props.RequireFullSync = proto.Bool(true)
	if props.HistorySyncConfig == nil {
		props.HistorySyncConfig = &waCompanionReg.DeviceProps_HistorySyncConfig{}
	}
	if maxMB > 0 {
		props.HistorySyncConfig.FullSyncSizeMbLimit = proto.Uint32(maxMB)
	}
	if raw, err := proto.Marshal(props); err == nil {
		payload.DevicePairingData.DeviceProps = raw
	}
	return payload
}

// historyNotification reports whether evt is the phone's history-sync
// protocol message (sent to our own account).
func historyNotification(evt any) (*events.Message, *waE2E.HistorySyncNotification, bool) {
	msg, ok := evt.(*events.Message)
	if !ok || !msg.Info.IsFromMe {
		return nil, nil, false
	}
	notif := msg.Message.GetProtocolMessage().GetHistorySyncNotification()
	return msg, notif, notif != nil
}

// acceptHistory hands an intercepted notification to the inbox.
func (m *Manager) acceptHistory(ctx context.Context, ms *ManagedSession, msg *events.Message,
	notif *waE2E.HistorySyncNotification) {
	inbox := m.historyInbox()
	if inbox == nil {
		return
	}
	ms.mu.Lock()
	client := ms.client
	device := ms.device
	var days uint32
	if ms.assignedConfig != nil {
		days = ms.assignedConfig.HistorySyncDays
	}
	ms.mu.Unlock()
	hc, _ := client.(historysync.Client)
	account := ""
	if device != nil && device.ID != nil {
		account = device.ID.ToNonAD().String()
	}
	err := inbox.Accept(ctx, hc, historysync.Intercepted{
		SessionID: ms.SessionID, OrganizationID: ms.OrganizationID, MessageID: msg.Info.ID,
		AccountJID: account, NotifiedAt: msg.Info.Timestamp, Notification: notif, HistorySyncDays: days,
	})
	if err != nil {
		m.log.Warn("history notification not stored; left unacknowledged", "session", ms.SessionID, "err", err)
	}
}

// HistoryClient returns a session's connected client for the history worker.
// known=false means the session is not on this gateway anymore.
func (m *Manager) HistoryClient(id string) (historysync.Client, string, bool) {
	ms := m.Get(id)
	if ms == nil {
		return nil, "", false
	}
	ms.mu.Lock()
	client := ms.client
	device := ms.device
	ms.mu.Unlock()
	account := ""
	if device != nil && device.ID != nil {
		account = device.ID.ToNonAD().String()
	}
	hc, ok := client.(historysync.Client)
	if !ok || client == nil || !client.IsConnected() {
		return nil, account, true
	}
	return hc, account, true
}
