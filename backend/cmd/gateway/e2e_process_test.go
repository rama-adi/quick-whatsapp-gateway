package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
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
	"github.com/rama-adi/quick-whatsapp-gateway/internal/fakewhatsapp"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/controlclient"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/controlsupervisor"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/desiredstate"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/journal"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/waadapter"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki/gatewayidentity"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/inbound"
	sqlitestore "github.com/rama-adi/quick-whatsapp-gateway/internal/wa/store/sqlite"
	"go.mau.fi/whatsmeow/proto/waAdv"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	faults := &e2eGatewayFaults{}
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
	fakeURL := required("FAKE_URL")
	adapter := &journal.ControlAdapter{Journal: ledger, GatewayID: gatewayID}
	var reconciler *desiredstate.Reconciler
	sink := controlEventSink{adapter: adapter, assignment: func(event domain.Event) (uint64, bool) {
		if reconciler == nil {
			return 0, false
		}
		epoch, ok := reconciler.CurrentEpoch(event.Session)
		return epoch, ok && reconciler.OwnsSession(event.Organization, event.Session, epoch)
	}, log: slog.Default()}
	manager := wa.NewManager(keyStore, managedControlEventSink{sink}, nil, nil, slog.Default(), wa.Config{GatewayID: gatewayID})
	manager.SetClientFactory(func(device *wastore.Device) wa.Client {
		return fakewhatsapp.NewClient(device, fakeURL)
	})
	reconciler = desiredstate.New(manager, nil)
	engine := newPrivateEngine(gatewayID, manager, reconciler, waadapter.NewRoutingWAClient(manager), ledger)
	stopEngine, err := startPrivateEngine(engineAddr, gatewayID, identity, engine,
		grpc.UnaryInterceptor(faults.interceptResponse),
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
		StartedAt: time.Now(), Runtime: runtime, EventJournal: e2eFaultJournal{ControlAdapter: adapter, faults: faults},
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
	manager.SetInboundHandler(waadapter.NewInboundPipelineHandler(pipeline, slog.Default()))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		state := supervisor.Status()
		if !state.Ready || !state.DesiredStateHealthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(state)
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
		case "none", "drop_response", "drop_event_ack":
		default:
			http.Error(w, "unknown fault", 400)
			return
		}
		faults.setMode(request.Mode)
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

}

type e2eGatewayFaults struct {
	mu   sync.Mutex
	mode string
}

func (f *e2eGatewayFaults) setMode(mode string) {
	f.mu.Lock()
	f.mode = mode
	f.mu.Unlock()
}

func (s *e2eGatewayFaults) interceptResponse(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
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
	faults *e2eGatewayFaults
}

func (j e2eFaultJournal) AckEvents(ctx context.Context, sequence uint64) error {
	j.faults.mu.Lock()
	drop := j.faults.mode == "drop_event_ack"
	if drop {
		j.faults.mode = "none"
	}
	j.faults.mu.Unlock()
	if drop {
		return errors.New("simulated lost committed event acknowledgement")
	}
	return j.ControlAdapter.AckEvents(ctx, sequence)
}
