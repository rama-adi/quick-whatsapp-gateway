package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/controlclient"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/controlsupervisor"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/desiredstate"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/journal"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/waadapter"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki/gatewayidentity"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/inbound"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
	sqlitestore "github.com/rama-adi/quick-whatsapp-gateway/internal/wa/store/sqlite"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waE2E"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waevents "go.mau.fi/whatsmeow/types/events"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestE2EGatewayProcess is a child process for the isolated network suite. It is
// deliberately test-only: production binaries cannot enable its fault controls.
// The real dispatcher builds WhatsApp protobufs and the real engine commits its
// SQLite ledger. Only device connection and external send/upload are simulated.
func TestE2EGatewayProcess(t *testing.T) {
	if os.Getenv("QWG_E2E_GATEWAY") != "1" {
		t.Skip("child process for isolated E2E suite")
	}
	required := func(key string) string {
		value := os.Getenv("QWG_E2E_" + key)
		if value == "" {
			t.Fatalf("QWG_E2E_%s is required", key)
		}
		return value
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	gatewayID, sessionID, orgID := required("GATEWAY_ID"), required("SESSION_ID"), required("ORG_ID")
	engineAddr, controlAddr := required("ENGINE_ADDR"), required("CONTROL_ADDR")
	deviceJID, err := types.ParseJID(required("DEVICE_JID"))
	if err != nil {
		t.Fatal(err)
	}
	deviceLID, err := types.ParseJID(required("DEVICE_LID"))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(required("BOOTSTRAP_CA"))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := gatewayidentity.New(gatewayidentity.Config{
		Directory: required("CREDENTIAL_DIR"), GatewayID: gatewayID, BootstrapCA: ca,
	})
	if err != nil {
		t.Fatal(err)
	}
	control, err := controlclient.New(controlclient.Config{
		Target: required("API_GRPC"), GatewayID: gatewayID, Identity: identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if err := control.Ensure(ctx, os.Getenv("QWG_E2E_ENROLLMENT_TOKEN")); err != nil {
		t.Fatal(err)
	}
	journalPath := required("JOURNAL_PATH")
	ledger, err := journal.Open(ctx, journalPath, journal.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	transport := &e2eWhatsApp{path: filepath.Join(filepath.Dir(journalPath), "captures.jsonl"), captures: []e2eCapture{}}
	if err := transport.load(); err != nil {
		t.Fatal(err)
	}
	keyStore, err := sqlitestore.Open(ctx, "file:"+filepath.Join(filepath.Dir(journalPath), "device.db")+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer keyStore.Close()
	device, err := keyStore.GetDevice(ctx, deviceJID)
	if err != nil {
		t.Fatal(err)
	}
	if device == nil {
		device = keyStore.NewDevice()
		device.ID = &deviceJID
		device.LID = deviceLID
		device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{}, AccountSignature: make([]byte, ed25519.SignatureSize), AccountSignatureKey: make([]byte, ed25519.PublicKeySize), DeviceSignature: make([]byte, ed25519.SignatureSize)}
		if err := device.Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	transport.device = device
	protocolClient := outbound.NewWhatsmeowClientWithTransport(whatsmeow.NewClient(device, nil), transport)
	adapter := &journal.ControlAdapter{Journal: ledger, GatewayID: gatewayID}
	var reconciler *desiredstate.Reconciler
	sink := controlEventSink{adapter: adapter, assignment: func(org, session string) (uint64, bool) {
		if reconciler == nil {
			return 0, false
		}
		epoch, ok := reconciler.CurrentEpoch(session)
		return epoch, ok && reconciler.OwnsSession(org, session, epoch)
	}, log: slog.Default()}
	manager := wa.NewManager(keyStore, managedControlEventSink{sink}, nil, nil, slog.Default(), wa.Config{GatewayID: gatewayID})
	manager.SetClientFactory(func(device *wastore.Device) wa.Client {
		return &e2eDeviceClient{Client: whatsmeow.NewClient(device, nil), transport: transport, groups: map[types.JID]*types.GroupInfo{}, blocked: map[types.JID]bool{}}
	})
	reconciler = desiredstate.New(manager, nil)
	engine := newPrivateEngine(gatewayID, manager, reconciler, protocolClient, ledger)
	stopEngine, err := startPrivateEngine(engineAddr, gatewayID, identity, engine,
		grpc.UnaryInterceptor(transport.interceptResponse),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer stopEngine()
	opener, err := controlsupervisor.NewCurrentConnOpener(control)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newGatewayControlRuntime(slog.Default())
	runtime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY)
	runtime.setSessionCounter(func(context.Context) (int, error) { return reconciler.AssignmentCount(), nil })
	instanceID, err := newGatewayInstanceID()
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := controlsupervisor.New(controlsupervisor.Config{
		InstanceID: instanceID, SoftwareVersion: "isolated-e2e", GRPCEndpoint: engineAddr,
		StartedAt: time.Now(), Runtime: runtime, EventJournal: e2eFaultJournal{ControlAdapter: adapter, transport: transport},
	}, opener)
	if err != nil {
		t.Fatal(err)
	}
	applier := &desiredstate.ControlApplier{Reconciler: reconciler}
	defer applier.Stop()
	supervisor.SetDesiredState(applier)
	go func() {
		if err := supervisor.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("E2E control supervisor", "err", err)
			cancel()
		}
	}()
	lifecycle := &managerLifecycleState{}
	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			lifecycle.markTerminal()
			if err := manager.Shutdown(context.Background()); err != nil {
				slog.Warn("E2E manager shutdown", "err", err)
			}
		})
	}
	defer shutdown()
	disabled := make(chan struct{}, 1)
	go runControlDirectives(ctx, supervisor, runtime, lifecycle, shutdown,
		func() gatewayv1.GatewayRuntimeState { return gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY }, disabled, slog.Default())
	go func() {
		select {
		case <-disabled:
			cancel()
		case <-ctx.Done():
		}
	}()
	pipeline := inbound.NewPipeline(waadapter.NewInboundNormalizer(manager.LiveOps(), nil), inbound.NewNoopCommandRegistry(),
		inbound.NoopRepos{}, sink, controlWebhookSink{}, nil, inbound.SystemClock{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		state := supervisor.Status()
		if !state.Ready || !state.DesiredStateHealthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(state)
	})
	mux.HandleFunc("GET /operations", func(w http.ResponseWriter, r *http.Request) {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		_ = json.NewEncoder(w).Encode(transport.operations)
	})
	mux.HandleFunc("GET /captures", func(w http.ResponseWriter, r *http.Request) {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		_ = json.NewEncoder(w).Encode(transport.captures)
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": transport.mode, "blocked": transport.blocked, "captures": len(transport.captures), "attempts": transport.attempts})
	})
	mux.HandleFunc("POST /fault", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		switch request.Mode {
		case "none", "send_error", "server_405", "upload_error", "block_send", "drop_response", "drop_event_ack":
		default:
			http.Error(w, "unknown fault", 400)
			return
		}
		transport.mu.Lock()
		defer transport.mu.Unlock()
		if transport.release != nil {
			close(transport.release)
			transport.release = nil
		}
		transport.mode = request.Mode
		if request.Mode == "block_send" {
			transport.release = make(chan struct{})
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /release", func(w http.ResponseWriter, r *http.Request) {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		if transport.release != nil {
			close(transport.release)
			transport.release = nil
		}
		transport.mode = "none"
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /events", func(w http.ResponseWriter, r *http.Request) {
		var event domain.Event
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if event.Session == "" {
			event.Session = sessionID
		}
		if event.Organization == "" {
			event.Organization = orgID
		}
		if event.ID == "" {
			event.ID = domain.NewEventID()
		}
		if event.Schema == "" {
			event.Schema = domain.Schema
		}
		if event.Timestamp == 0 {
			event.Timestamp = domain.NowMs()
		}
		if err := sink.Publish(r.Context(), event); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		supervisor.ReportNow()
		_ = json.NewEncoder(w).Encode(event)
	})
	mux.HandleFunc("POST /incoming", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID        string          `json:"id"`
			Chat      string          `json:"chat"`
			Sender    string          `json:"sender"`
			SenderAlt string          `json:"senderAlt"`
			FromMe    bool            `json:"fromMe"`
			Timestamp int64           `json:"timestamp"`
			Message   json.RawMessage `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		chat, err := types.ParseJID(request.Chat)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		sender, err := types.ParseJID(request.Sender)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		senderAlt, _ := types.ParseJID(request.SenderAlt)
		message := &waE2E.Message{}
		if err := protojson.Unmarshal(request.Message, message); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		timestamp := time.Now()
		if request.Timestamp != 0 {
			timestamp = time.UnixMilli(request.Timestamp)
		}
		raw := &waevents.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{
			Chat: chat, Sender: sender, SenderAlt: senderAlt, IsFromMe: request.FromMe, IsGroup: chat.Server == types.GroupServer,
		}, ID: request.ID, Timestamp: timestamp}, Message: message}
		if err := pipeline.Process(r.Context(), sessionID, orgID, false, raw); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		supervisor.ReportNow()
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: controlAddr, Handler: mux}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("E2E controls", "err", err)
			cancel()
		}
	}()
	<-ctx.Done()
	// Parent controls process lifetime. Close unblocks HTTP requests without an
	// invented shutdown deadline; engine RPC cancellation follows process teardown.
	_ = server.Close()
	transport.mu.Lock()
	if transport.release != nil {
		close(transport.release)
		transport.release = nil
	}
	transport.mu.Unlock()
}

type e2eCapture struct {
	ID      string          `json:"id"`
	To      string          `json:"to"`
	Message json.RawMessage `json:"message"`
}
type e2eWhatsApp struct {
	device     *wastore.Device
	mu         sync.Mutex
	path       string
	captures   []e2eCapture
	mode       string
	release    chan struct{}
	blocked    int
	attempts   int
	operations []map[string]any
}

func (s *e2eWhatsApp) load() error {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	for {
		var capture e2eCapture
		err := decoder.Decode(&capture)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		s.captures = append(s.captures, capture)
	}
}
func (s *e2eWhatsApp) SendMessage(ctx context.Context, to types.JID, message *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	s.mu.Lock()
	s.attempts++
	mode, release := s.mode, s.release
	if release != nil {
		s.blocked++
	}
	s.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			s.mu.Lock()
			s.blocked--
			s.mu.Unlock()
			return whatsmeow.SendResponse{}, ctx.Err()
		}
		s.mu.Lock()
		s.blocked--
		s.mu.Unlock()
	}
	if mode == "send_error" {
		return whatsmeow.SendResponse{}, errors.New("simulated WhatsApp disconnect before acknowledgement")
	}
	if mode == "server_405" {
		return whatsmeow.SendResponse{}, fmt.Errorf("%w 405", whatsmeow.ErrServerReturnedError)
	}
	raw, err := protojson.Marshal(message)
	if err != nil {
		return whatsmeow.SendResponse{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	capture := e2eCapture{ID: fmt.Sprintf("E2E_%d", len(s.captures)+1), To: to.String(), Message: raw}
	if len(extra) == 1 && extra[0].ID != "" {
		capture.ID = string(extra[0].ID)
	}
	if secret := message.GetMessageContextInfo().GetMessageSecret(); len(secret) > 0 {
		sender := s.device.ID.ToNonAD()
		if to.Server == types.GroupServer || to.Server == types.HiddenUserServer {
			sender = s.device.LID
		}
		if err := s.device.MsgSecrets.PutMessageSecret(ctx, to, sender, capture.ID, secret); err != nil {
			return whatsmeow.SendResponse{}, err
		}
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return whatsmeow.SendResponse{}, err
	}
	err = json.NewEncoder(file).Encode(capture)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return whatsmeow.SendResponse{}, err
	}
	if closeErr != nil {
		return whatsmeow.SendResponse{}, closeErr
	}
	s.captures = append(s.captures, capture)
	return whatsmeow.SendResponse{ID: capture.ID, Timestamp: time.Now()}, nil
}
func (s *e2eWhatsApp) Upload(_ context.Context, data []byte, _ whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	s.mu.Lock()
	mode := s.mode
	s.mu.Unlock()
	if mode == "upload_error" {
		return whatsmeow.UploadResponse{}, errors.New("simulated upload failure")
	}
	sum := sha256.Sum256(data)
	return whatsmeow.UploadResponse{URL: "https://isolated.invalid/media", DirectPath: "/media", FileLength: uint64(len(data)), FileSHA256: sum[:], FileEncSHA256: sum[:], MediaKey: sum[:]}, nil
}
func (s *e2eWhatsApp) interceptResponse(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	response, err := handler(ctx, req)
	if err != nil || (!strings.HasSuffix(info.FullMethod, "/SendMessage") && !strings.HasSuffix(info.FullMethod, "/MessageOp")) {
		return response, err
	}
	s.mu.Lock()
	drop := s.mode == "drop_response"
	if drop {
		s.mode = "none"
	}
	s.mu.Unlock()
	if drop {
		return nil, status.Error(codes.Unavailable, "simulated lost response after durable gateway commit")
	}
	return response, nil
}

// An API commit followed by a lost acknowledgement must replay the same journal
// event. This fails before the local cursor advances, forcing real reconnect.
type e2eFaultJournal struct {
	*journal.ControlAdapter
	transport *e2eWhatsApp
}

func (j e2eFaultJournal) AckEvents(ctx context.Context, sequence uint64) error {
	j.transport.mu.Lock()
	drop := j.transport.mode == "drop_event_ack"
	if drop {
		j.transport.mode = "none"
	}
	j.transport.mu.Unlock()
	if drop {
		return errors.New("simulated lost committed event acknowledgement")
	}
	return j.ControlAdapter.AckEvents(ctx, sequence)
}
