package historysync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	waevents "go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/journal"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fakeHistoryClient struct {
	mu           sync.Mutex
	parse        Parser
	blobs        map[string]*waHistorySync.HistorySync // by DirectPath
	downloadErr  error
	downloads    int
	receipts     []string
	serverErrors []string
	deleted      []string
}

func (f *fakeHistoryClient) DownloadHistorySync(_ context.Context, n *waE2E.HistorySyncNotification, sync bool) (*waHistorySync.HistorySync, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads++
	if !sync {
		return nil, errors.New("worker must request synchronous storage")
	}
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	blob, ok := f.blobs[n.GetDirectPath()]
	if !ok {
		return nil, fmt.Errorf("no blob at %q", n.GetDirectPath())
	}
	return blob, nil
}
func (f *fakeHistoryClient) ParseWebMessage(chat types.JID, msg *waWeb.WebMessageInfo) (*waevents.Message, error) {
	return f.parse(chat, msg)
}
func (f *fakeHistoryClient) SendProtocolMessageReceipt(_ context.Context, id types.MessageID, typ types.ReceiptType) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if typ != types.ReceiptTypeHistorySync {
		return errors.New("wrong receipt type")
	}
	f.receipts = append(f.receipts, string(id))
	return nil
}
func (f *fakeHistoryClient) SendHistorySyncServerErrorReceipt(_ context.Context, id types.MessageID, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serverErrors = append(f.serverErrors, string(id))
	return nil
}
func (f *fakeHistoryClient) DeleteMedia(_ context.Context, _ whatsmeow.MediaType, path string, _ []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, path)
	return nil
}

type fakeSessions struct {
	mu      sync.Mutex
	client  Client
	account string
	known   bool
}

func (s *fakeSessions) HistoryClient(string) (Client, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client, s.account, s.known
}

type recordingSink struct {
	mu     sync.Mutex
	events []domain.Event
	fail   func(domain.Event) error
}

func (s *recordingSink) Publish(_ context.Context, e domain.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		if err := s.fail(e); err != nil {
			return err
		}
	}
	s.events = append(s.events, e)
	return nil
}

func (s *recordingSink) phases() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.events {
		switch p := e.Payload.(type) {
		case apitypes.HistorySyncPayload:
			out = append(out, p.Phase)
		case apitypes.HistoryBatchPayload:
			out = append(out, fmt.Sprintf("batch:%s:%d/%d", p.SyncType, p.Part, p.Parts))
		}
	}
	return out
}

func (s *recordingSink) last() apitypes.HistorySyncPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events[len(s.events)-1].Payload.(apitypes.HistorySyncPayload)
}

type harness struct {
	t        *testing.T
	journal  *journal.Journal
	client   *fakeHistoryClient
	sessions *fakeSessions
	sink     *recordingSink
	clock    *fakeClock
	worker   *Worker
	owned    bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	j, err := journal.Open(context.Background(), filepath.Join(t.TempDir(), "journal.db"),
		journal.Config{MaxBytes: 256 << 20, VolumeBytes: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	h := &harness{
		t: t, journal: j, client: &fakeHistoryClient{parse: testParser(t), blobs: map[string]*waHistorySync.HistorySync{}},
		sink: &recordingSink{}, clock: &fakeClock{now: time.Now().UTC()}, owned: true,
	}
	h.sessions = &fakeSessions{client: h.client, account: ownJID, known: true}
	h.worker = NewWorker(j, h.sessions, h.sink, Config{
		BatchInterval: time.Millisecond, Now: h.clock.Now,
		Owns: func(string, string) bool { return h.owned },
	}, nil)
	return h
}

func (h *harness) accept(id, path string, syncType waE2E.HistorySyncType, days uint32) {
	h.t.Helper()
	err := h.worker.Accept(context.Background(), h.client, Intercepted{
		SessionID: "sess_1", OrganizationID: "org_1", MessageID: id, AccountJID: ownJID,
		NotifiedAt: h.clock.Now(), HistorySyncDays: days,
		Notification: &waE2E.HistorySyncNotification{DirectPath: proto.String(path), SyncType: syncType.Enum()},
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

// drain runs worker passes until nothing is due.
func (h *harness) drain() {
	h.t.Helper()
	for i := 0; i < 50; i++ {
		if busy, _ := h.worker.tick(context.Background()); !busy {
			return
		}
	}
	h.t.Fatal("worker never went idle")
}

func (h *harness) pending() []journal.HistoryInboxRow {
	rows, err := h.journal.PendingHistory(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return rows
}

func dmBlob(syncType waHistorySync.HistorySync_HistorySyncType, progress *uint32, prefix string, n int) *waHistorySync.HistorySync {
	base := uint64(time.Now().Unix()) - 3600
	conv := &waHistorySync.Conversation{ID: proto.String(alicePN)}
	for i := 0; i < n; i++ {
		conv.Messages = append(conv.Messages, hmsg(uint64(i), webMsg(fmt.Sprintf("%s%d", prefix, i), alicePN, false, "", base+uint64(i), text("hi"))))
	}
	return &waHistorySync.HistorySync{SyncType: syncType.Enum(), Progress: progress, Conversations: []*waHistorySync.Conversation{conv}}
}

func TestWorkerImportsRunToCompletion(t *testing.T) {
	h := newHarness(t)
	h.client.blobs["/boot"] = dmBlob(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, nil, "B", 250)
	h.client.blobs["/recent"] = dmBlob(waHistorySync.HistorySync_RECENT, proto.Uint32(100), "R", 10)
	h.accept("N1", "/boot", waE2E.HistorySyncType_INITIAL_BOOTSTRAP, 30)
	h.accept("N1", "/boot", waE2E.HistorySyncType_INITIAL_BOOTSTRAP, 30) // phone resent: stored once, acked again
	h.accept("N2", "/recent", waE2E.HistorySyncType_RECENT, 30)
	if got := strings.Join(h.client.receipts, ","); got != "N1,N1,N2" {
		t.Fatalf("receipts = %s", got)
	}
	if rows := h.pending(); len(rows) != 2 || rows[0].CutoffMs == 0 || !rows[0].ImportEnabled {
		t.Fatalf("pending = %+v", rows)
	}
	h.drain()
	want := "started,batch:initial_bootstrap:1/2,batch:initial_bootstrap:2/2,progress,batch:recent:1/1,progress,completed"
	if got := strings.Join(h.sink.phases(), ","); got != want {
		t.Fatalf("events = %s", got)
	}
	final := h.sink.last()
	if final.Chunks != 2 || final.Batches != 3 || final.Messages != 260 || final.Conversations != 2 ||
		final.Progress == nil || *final.Progress != 100 || final.SyncType != "recent" || final.AccountJID != ownJID ||
		final.CutoffAt == 0 || !strings.HasPrefix(final.RunID, "hsr_") {
		t.Fatalf("completed = %+v", final)
	}
	for _, e := range h.sink.events {
		if e.Session != "sess_1" || e.Organization != "org_1" || !strings.HasPrefix(e.ID, domain.PrefixEvent) {
			t.Fatalf("event envelope = %+v", e)
		}
		if b, ok := e.Payload.(apitypes.HistoryBatchPayload); ok && b.RunID != final.RunID {
			t.Fatal("batch carries another run id")
		}
	}
	if strings.Join(h.client.deleted, ",") != "/boot,/recent" {
		t.Fatalf("deleted = %v", h.client.deleted)
	}
	if len(h.pending()) != 0 {
		t.Fatal("inbox not drained")
	}
}

func TestWorkerResumesFromCursorWithSameIDs(t *testing.T) {
	h := newHarness(t)
	h.client.blobs["/boot"] = dmBlob(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, nil, "B", 450)
	h.accept("N1", "/boot", waE2E.HistorySyncType_INITIAL_BOOTSTRAP, 30)
	batches := 0
	h.sink.fail = func(e domain.Event) error {
		if e.Type == domain.EventHistoryBatch {
			batches++
			if batches == 2 {
				return errors.New("journal full")
			}
		}
		return nil
	}
	h.drain()
	rows := h.pending()
	if len(rows) != 1 || rows[0].BatchesDone != 1 {
		t.Fatalf("cursor = %+v", rows)
	}
	h.sink.fail = nil
	h.clock.Advance(time.Minute)
	h.drain()
	expected := Convert(Input{
		SessionID: "sess_1", NotificationID: "N1", NotifiedAt: rows[0].NotifiedAt, AccountJID: ownJID,
		Blob: h.client.blobs["/boot"], Parse: h.client.parse,
	})
	var ids []string
	for _, e := range h.sink.events {
		if e.Type == domain.EventHistoryBatch {
			ids = append(ids, e.ID)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("batch events = %d", len(ids))
	}
	for i, b := range expected.Batches {
		if ids[i] != b.EventID {
			t.Fatalf("batch %d id %s, want deterministic %s", i, ids[i], b.EventID)
		}
	}
}

func TestWorkerImportOffStillDownloads(t *testing.T) {
	h := newHarness(t)
	h.client.blobs["/boot"] = dmBlob(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, nil, "B", 5)
	h.accept("N1", "/boot", waE2E.HistorySyncType_INITIAL_BOOTSTRAP, 0)
	h.drain()
	if h.client.downloads != 1 || len(h.sink.events) != 0 || len(h.pending()) != 0 {
		t.Fatalf("downloads=%d events=%d pending=%d", h.client.downloads, len(h.sink.events), len(h.pending()))
	}
}

func TestWorkerExpiredBlobAsksPhoneOnce(t *testing.T) {
	h := newHarness(t)
	h.client.downloadErr = fmt.Errorf("failed to download: %w", whatsmeow.ErrMediaDownloadFailedWith404)
	h.accept("N1", "/gone", waE2E.HistorySyncType_RECENT, 30)
	h.drain()
	if strings.Join(h.client.serverErrors, ",") != "N1" || len(h.pending()) != 0 || len(h.sink.events) != 0 {
		t.Fatalf("server errors=%v pending=%d events=%d", h.client.serverErrors, len(h.pending()), len(h.sink.events))
	}
}

func TestWorkerRetriesThenFailsRun(t *testing.T) {
	h := newHarness(t)
	h.client.blobs["/boot"] = dmBlob(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, nil, "B", 3)
	h.accept("N1", "/boot", waE2E.HistorySyncType_INITIAL_BOOTSTRAP, 30)
	h.drain()
	h.client.downloadErr = errors.New("connection reset")
	h.accept("N2", "/recent", waE2E.HistorySyncType_RECENT, 30)
	h.drain()
	rows := h.pending()
	if len(rows) != 1 || rows[0].Attempts != 1 || !rows[0].NextAttemptAt.After(h.clock.Now()) {
		t.Fatalf("retry state = %+v", rows)
	}
	h.clock.Advance(25 * time.Hour)
	h.drain()
	if len(h.pending()) != 0 {
		t.Fatal("download retried past the cap")
	}
	final := h.sink.last()
	if final.Phase != domain.HistoryPhaseFailed || !strings.Contains(final.Error, "connection reset") || final.Chunks != 1 {
		t.Fatalf("final = %+v", final)
	}
}

func TestWorkerRelinkDiscardsOtherAccount(t *testing.T) {
	h := newHarness(t)
	h.client.blobs["/boot"] = dmBlob(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, nil, "B", 3)
	h.accept("N1", "/boot", waE2E.HistorySyncType_INITIAL_BOOTSTRAP, 30)
	h.drain()
	h.accept("N2", "/recent", waE2E.HistorySyncType_RECENT, 30)
	h.worker.Paired(context.Background(), "sess_1", "15559999999@s.whatsapp.net")
	h.drain()
	if len(h.pending()) != 0 || h.client.downloads != 1 {
		t.Fatalf("pending=%d downloads=%d", len(h.pending()), h.client.downloads)
	}
	if final := h.sink.last(); final.Phase != domain.HistoryPhaseFailed || final.Error != "device relinked to another account" {
		t.Fatalf("final = %+v", final)
	}
	// A notification stored for the old account after the switch is dropped
	// by the worker's own account check.
	h.sessions.account = "15559999999@s.whatsapp.net"
	h.accept("N3", "/boot", waE2E.HistorySyncType_RECENT, 30)
	h.drain()
	if len(h.pending()) != 0 || h.client.downloads != 1 {
		t.Fatal("old-account notification was processed")
	}
}

func TestWorkerWaitsForOwnershipAndCompletesIdleRuns(t *testing.T) {
	h := newHarness(t)
	h.client.blobs["/boot"] = dmBlob(waHistorySync.HistorySync_INITIAL_BOOTSTRAP, nil, "B", 3)
	h.owned = false
	h.accept("N1", "/boot", waE2E.HistorySyncType_INITIAL_BOOTSTRAP, 30)
	h.drain()
	if h.client.downloads != 0 || len(h.pending()) != 1 {
		t.Fatal("processed a session this gateway does not own")
	}
	h.owned = true
	h.clock.Advance(time.Minute)
	h.drain()
	if got := strings.Join(h.sink.phases(), ","); got != "started,batch:initial_bootstrap:1/1,progress" {
		t.Fatalf("events = %s", got)
	}
	h.clock.Advance(11 * time.Minute)
	h.drain()
	if final := h.sink.last(); final.Phase != domain.HistoryPhaseCompleted || final.Messages != 3 {
		t.Fatalf("final = %+v", final)
	}
}

func TestWorkerAssignmentDaysOverrideRuntimeConfig(t *testing.T) {
	h := newHarness(t)
	h.worker.cfg.Days = func(string) (uint32, bool) { return 7, true }
	h.accept("N1", "/boot", waE2E.HistorySyncType_INITIAL_BOOTSTRAP, 0)
	rows := h.pending()
	want := h.clock.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	if len(rows) != 1 || !rows[0].ImportEnabled || rows[0].CutoffMs != want {
		t.Fatalf("row = %+v, want cutoff %d", rows, want)
	}
}

func TestWorkerAcceptLeavesUnackedOnStoreFailure(t *testing.T) {
	h := newHarness(t)
	_ = h.journal.Close()
	err := h.worker.Accept(context.Background(), h.client, Intercepted{
		SessionID: "sess_1", MessageID: "N1", Notification: &waE2E.HistorySyncNotification{},
	})
	if err == nil || len(h.client.receipts) != 0 {
		t.Fatalf("err=%v receipts=%v", err, h.client.receipts)
	}
}

// TestMaxSizeBatchSurvivesControlDecode journals the largest batch Convert
// can produce (one truncated message plus a large group's metadata) and reads
// it back through the real control-stream decode, which rejects any event
// over the 1 MiB control batch.
func TestMaxSizeBatchSurvivesControlDecode(t *testing.T) {
	base := uint64(time.Now().Unix()) - 60
	conv := &waHistorySync.Conversation{ID: proto.String(groupJID), Name: proto.String("Big group")}
	for i := 0; i < 1024; i++ {
		conv.Participant = append(conv.Participant, &waHistorySync.GroupParticipant{
			UserJID: proto.String(fmt.Sprintf("9000000%08d@lid", i))})
	}
	conv.Messages = []*waHistorySync.HistorySyncMsg{hmsg(1, webMsg("HUGE", groupJID, false, bobLID, base,
		&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:        proto.String(strings.Repeat("é", 3*1024*1024)),
			ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("Q"), QuotedMessage: text(strings.Repeat("q", 512*1024))},
		}}))}
	res := Convert(input(t, &waHistorySync.HistorySync{SyncType: waHistorySync.HistorySync_RECENT.Enum(),
		Conversations: []*waHistorySync.Conversation{conv}}))
	if len(res.Batches) != 1 || !res.Batches[0].Payload.Conversations[0].Messages[0].Truncated {
		t.Fatalf("expected one truncated batch, got %d", len(res.Batches))
	}
	j, err := journal.Open(context.Background(), filepath.Join(t.TempDir(), "journal.db"),
		journal.Config{MaxBytes: 256 << 20, VolumeBytes: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	adapter := &journal.ControlAdapter{Journal: j, GatewayID: "gw-1"}
	event := domain.Event{Schema: domain.Schema, ID: res.Batches[0].EventID, Type: domain.EventHistoryBatch,
		Session: "sess_1", Organization: "org_1", Timestamp: time.Now().UnixMilli(), Payload: res.Batches[0].Payload}
	if _, _, err := adapter.AppendDomainEvent(context.Background(), event, 7); err != nil {
		t.Fatal(err)
	}
	adapter.SetDesiredAssignments(&gatewayv1.DesiredStateSnapshot{Revision: 1, Assignments: []*gatewayv1.SessionAssignment{
		{SessionId: "sess_1", OrganizationId: "org_1", AssignmentEpoch: 7},
	}})
	batch, err := adapter.NextEventBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("control decode rejected the batch: %v", err)
	}
	if len(batch.Events) != 1 || proto.Size(batch) > int(journal.DefaultBatchBytes) {
		t.Fatalf("events=%d size=%d", len(batch.Events), proto.Size(batch))
	}
	var st structpb.Struct
	if err := proto.Unmarshal(batch.Events[0].Payload, &st); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(st.AsMap()["payload"])
	var decoded apitypes.HistoryBatchPayload
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.Conversations[0].Messages[0]
	if got.WAMessageID != "HUGE" || got.Body != res.Batches[0].Payload.Conversations[0].Messages[0].Body ||
		len(decoded.Conversations[0].Participants) != 1024 {
		t.Fatal("batch changed through the control decode")
	}
}
