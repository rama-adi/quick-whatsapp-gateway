package wa

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/keys"
	"google.golang.org/protobuf/proto"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/desiredstate"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/historysync"
)

type recordingInbox struct {
	mu        sync.Mutex
	accepted  []historysync.Intercepted
	client    historysync.Client
	paired    []string
	discarded []string
}

func (r *recordingInbox) Accept(_ context.Context, c historysync.Client, n historysync.Intercepted) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accepted = append(r.accepted, n)
	r.client = c
	return nil
}
func (r *recordingInbox) Paired(_ context.Context, sessionID, accountJID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paired = append(r.paired, sessionID+"="+accountJID)
}
func (r *recordingInbox) Discard(_ context.Context, sessionID, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discarded = append(r.discarded, sessionID+":"+reason)
}

// historyFakeClient is a lifecycle fake that also satisfies historysync.Client.
type historyFakeClient struct{ fakeClient }

func (*historyFakeClient) DownloadHistorySync(context.Context, *waE2E.HistorySyncNotification, bool) (*waHistorySync.HistorySync, error) {
	return nil, nil
}
func (*historyFakeClient) ParseWebMessage(types.JID, *waWeb.WebMessageInfo) (*events.Message, error) {
	return nil, nil
}
func (*historyFakeClient) SendProtocolMessageReceipt(context.Context, types.MessageID, types.ReceiptType) error {
	return nil
}
func (*historyFakeClient) SendHistorySyncServerErrorReceipt(context.Context, types.MessageID, []byte) error {
	return nil
}
func (*historyFakeClient) DeleteMedia(context.Context, whatsmeow.MediaType, string, []byte, string) error {
	return nil
}

func historyEvent(fromMe bool) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{IsFromMe: fromMe},
			ID:            "HIST1", Timestamp: time.UnixMilli(1_760_000_000_000),
		},
		Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_HISTORY_SYNC_NOTIFICATION.Enum(),
			HistorySyncNotification: &waE2E.HistorySyncNotification{
				SyncType: waE2E.HistorySyncType_INITIAL_BOOTSTRAP.Enum(), ChunkOrder: proto.Uint32(1),
			},
		}},
	}
}

func TestHistoryNotificationBypassesInboundPipeline(t *testing.T) {
	m, _, inbound, _ := newTestManagerParts(t, Config{})
	inbox := &recordingInbox{}
	m.SetHistoryInbox(inbox)
	jid := types.NewJID("15550000001", types.DefaultUserServer)
	jid.Device = 7
	client := &historyFakeClient{}
	ms := &ManagedSession{
		SessionID: "sess_1", OrganizationID: "org_1", client: client, device: &store.Device{ID: &jid},
		assignedConfig: &desiredstate.Config{HistorySyncDays: 30},
	}
	h := m.eventHandlerFor(ms)
	h(historyEvent(true))
	if inbound.countValue() != 0 {
		t.Fatal("history notification reached the inbound pipeline")
	}
	if len(inbox.accepted) != 1 {
		t.Fatalf("accepted = %d", len(inbox.accepted))
	}
	got := inbox.accepted[0]
	if got.SessionID != "sess_1" || got.OrganizationID != "org_1" || got.MessageID != "HIST1" ||
		got.AccountJID != "15550000001@s.whatsapp.net" || got.HistorySyncDays != 30 || got.Notification == nil ||
		!got.NotifiedAt.Equal(time.UnixMilli(1_760_000_000_000)) {
		t.Fatalf("intercepted = %+v", got)
	}
	if inbox.client == nil {
		t.Fatal("inbox did not receive the client for the receipt")
	}
	// A history notification from someone else is not a protocol message for
	// us; it stays on the normal path.
	h(historyEvent(false))
	if inbound.countValue() != 1 || len(inbox.accepted) != 1 {
		t.Fatalf("foreign notification: inbound=%d accepted=%d", inbound.countValue(), len(inbox.accepted))
	}
}

func TestHistoryHooksOnPairLogoutAndForget(t *testing.T) {
	m, _, _, fc := newTestManagerParts(t, Config{})
	inbox := &recordingInbox{}
	m.SetHistoryInbox(inbox)
	ms := &ManagedSession{SessionID: "sess_1", OrganizationID: "org_1", client: fc, cancel: func() {}}
	m.mu.Lock()
	m.sessions["sess_1"] = ms
	m.mu.Unlock()
	h := m.eventHandlerFor(ms)
	h(&events.PairSuccess{ID: types.JID{User: "15550000009", Server: types.DefaultUserServer, Device: 3}})
	h(&events.LoggedOut{})
	m.Forget("sess_1")
	if len(inbox.paired) != 1 || inbox.paired[0] != "sess_1=15550000009@s.whatsapp.net" {
		t.Fatalf("paired = %v", inbox.paired)
	}
	want := []string{"sess_1:session logged out", "sess_1:session removed"}
	if len(inbox.discarded) != 2 || inbox.discarded[0] != want[0] || inbox.discarded[1] != want[1] {
		t.Fatalf("discarded = %v", inbox.discarded)
	}
}

func TestHistoryClientConfigurationAndFullSyncProps(t *testing.T) {
	device := &store.Device{
		IdentityKey: keys.NewKeyPair(), SignedPreKey: keys.NewKeyPair().CreateSignedPreKey(1), RegistrationID: 42,
	}
	m, _, _, _ := newTestManagerParts(t, Config{HistoryFullSync: true, HistoryFullSyncMaxMB: 512})
	m.newClient = func(d *store.Device) waClient { return whatsmeow.NewClient(d, nil) }
	if cli := m.buildClient(device).(*whatsmeow.Client); cli.ManualHistorySyncDownload {
		t.Fatal("manual history mode without an inbox would leave notifications unacknowledged")
	}
	m.SetHistoryInbox(&recordingInbox{})
	cli := m.buildClient(device).(*whatsmeow.Client)
	if !cli.ManualHistorySyncDownload || !cli.DisableManualHistorySyncReceipt || cli.GetClientPayload == nil {
		t.Fatal("history client not configured for manual download")
	}
	var props waCompanionReg.DeviceProps
	if err := proto.Unmarshal(cli.GetClientPayload().GetDevicePairingData().GetDeviceProps(), &props); err != nil {
		t.Fatal(err)
	}
	if !props.GetRequireFullSync() || props.GetHistorySyncConfig().GetFullSyncSizeMbLimit() != 512 {
		t.Fatalf("registration props = %+v", props.GetHistorySyncConfig())
	}
	if store.DeviceProps.GetRequireFullSync() || store.DeviceProps.GetHistorySyncConfig().FullSyncSizeMbLimit != nil {
		t.Fatal("full sync mutated whatsmeow's process-global DeviceProps")
	}
	jid := types.NewJID("15550000001", types.DefaultUserServer)
	device.ID = &jid
	if cli.GetClientPayload().GetDevicePairingData() != nil {
		t.Fatal("a paired device must log in, not register")
	}
}
