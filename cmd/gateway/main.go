// Command gateway is the gateway entrypoint and composition root: it loads
// configuration, opens the local SQLite whatsmeow keystore and event journal,
// wires the session manager over them, connects the mandatory mTLS control
// plane, and serves the private engine gRPC listener plus minimal operational
// probes with graceful shutdown. The gateway has no MySQL or Redis dependency:
// app-data writes are API-owned (derived from committed events), and every
// public operation executes API-locally over private engine RPCs.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/config"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/gateway/controlclient"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/gateway/controlsupervisor"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/gateway/desiredstate"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/gateway/journal"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/gateway/waadapter"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki/gatewayidentity"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/inbound"
)

var softwareVersion = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("gateway exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadGateway()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	setupLogging(cfg.LogLevel)
	log := slog.Default()

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- Mandatory control plane (fail-closed bootstrap) ---
	caInfo, statErr := os.Stat(cfg.BootstrapCAFile)
	if statErr != nil {
		return fmt.Errorf("stat gateway bootstrap CA: %w", statErr)
	}
	if !caInfo.Mode().IsRegular() || caInfo.Size() <= 0 || caInfo.Size() > 16<<10 {
		return errors.New("gateway bootstrap CA must be a regular file within 16 KiB")
	}
	bootstrapCA, readErr := os.ReadFile(cfg.BootstrapCAFile)
	if readErr != nil {
		return fmt.Errorf("read gateway bootstrap CA: %w", readErr)
	}
	controlIdentity, identityErr := gatewayidentity.New(gatewayidentity.Config{
		Directory:   cfg.CredentialDir,
		GatewayID:   cfg.GatewayID,
		BootstrapCA: bootstrapCA,
	})
	if identityErr != nil {
		return fmt.Errorf("build gateway identity: %w", identityErr)
	}
	control, err := controlclient.New(controlclient.Config{
		Target:    cfg.ControlPlaneAddr,
		GatewayID: cfg.GatewayID,
		Identity:  controlIdentity,
	})
	if err != nil {
		return fmt.Errorf("build gateway control client: %w", err)
	}
	if err = control.Ensure(ctx, cfg.EnrollmentToken); err != nil {
		return fmt.Errorf("connect gateway control plane: %w", err)
	}
	cfg.EnrollmentToken = ""
	_ = os.Unsetenv("GATEWAY_ENROLLMENT_TOKEN")
	opener, openerErr := controlsupervisor.NewCurrentConnOpener(control)
	if openerErr != nil {
		_ = control.Close()
		return fmt.Errorf("build gateway control stream: %w", openerErr)
	}
	instanceID, instanceErr := newGatewayInstanceID()
	if instanceErr != nil {
		_ = control.Close()
		return fmt.Errorf("create gateway process instance id: %w", instanceErr)
	}
	eventJournal, err := journal.Open(ctx, cfg.JournalPath, journal.DefaultConfig())
	if err != nil {
		return fmt.Errorf("open gateway event journal: %w", err)
	}
	defer func() {
		if closeErr := eventJournal.Close(); closeErr != nil {
			log.Warn("close gateway event journal", "err", closeErr)
		}
	}()
	controlRuntime := newGatewayControlRuntime(log)
	controlAdapter := &journal.ControlAdapter{Journal: eventJournal, GatewayID: cfg.GatewayID}

	var (
		supervisorCtx             context.Context
		cancelSupervisor          context.CancelFunc
		supervisorExited          chan struct{}
		supervisorResultMu        sync.Mutex
		supervisorResult          error
		supervisorStarted         bool
		certificateRenewalExpired chan error
		lifecycleDisabled         = make(chan struct{}, 1)
	)

	// --- whatsmeow keystore (gateway-local SQLite, §6.1) ---
	// Adoption is fail-closed: a missing/corrupt volume remains observable to
	// the control plane but cannot become a replacement device store.
	controlKeystore, err := openControlKeystore(ctx, cfg.WhatsmeowStoreDSN)
	if err != nil {
		return fmt.Errorf("inspect whatsmeow keystore: %w", err)
	}
	keystore := controlKeystore.holder
	if controlKeystore.Health().GetState() != gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY {
		controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED)
	}

	// --- Session manager (per-session whatsmeow clients) ---
	// Desired-state reconciliation owns session startup: the manager never reads
	// wa_sessions. Event fan-out is journal-only until the API commits each
	// envelope; there is no Redis publisher and no local webhook enqueue here.
	var desiredReconciler *desiredstate.Reconciler
	managerSink := managedControlEventSink{controlEventSink{
		adapter: controlAdapter,
		assignment: func(organizationID, sessionID string) (uint64, bool) {
			if desiredReconciler == nil {
				return 0, false
			}
			epoch, ok := desiredReconciler.CurrentEpoch(sessionID)
			return epoch, ok && desiredReconciler.OwnsSession(organizationID, sessionID, epoch)
		},
		log: log,
	}}
	inboundSink := controlEventSink{
		adapter: controlAdapter,
		assignment: func(organizationID, sessionID string) (uint64, bool) {
			if desiredReconciler == nil {
				return 0, false
			}
			epoch, ok := desiredReconciler.CurrentEpoch(sessionID)
			return epoch, ok && desiredReconciler.OwnsSession(organizationID, sessionID, epoch)
		},
		log: log,
	}
	manager := wa.NewManager(keystore, managerSink, nil, nil, log, wa.Config{
		GatewayID:           cfg.GatewayID,
		DeviceName:          cfg.WhatsAppDeviceName,
		InboundEventTimeout: 0,
	})
	inboundPipeline := inbound.NewPipeline(
		waadapter.NewInboundNormalizer(manager.LiveOps(), nil),
		inbound.NewNoopCommandRegistry(),
		inbound.NoopRepos{},
		inboundSink,
		controlWebhookSink{},
		manager.LiveOps(),
		inbound.SystemClock{},
		inbound.WithLogger(log),
		inbound.WithSessionConfig(func(sessionID string) (inbound.SessionConfig, bool) {
			sessionConfig, ok := manager.AssignedConfig(sessionID)
			if !ok {
				return inbound.SessionConfig{}, false
			}
			return inbound.SessionConfig{AutoRead: sessionConfig.AutoRead, PresenceTyping: sessionConfig.PresenceTyping}, true
		}),
	)
	inboundHandler := waadapter.NewInboundPipelineHandler(inboundPipeline, log)
	manager.SetInboundHandler(inboundHandler)

	desiredReconciler = desiredstate.New(manager, nil)
	controlRuntime.setSessionCounter(func(context.Context) (int, error) {
		return desiredReconciler.AssignmentCount(), nil
	})

	// --- Control supervisor (persistent stream owner) ---
	controlSupervisor, err := controlsupervisor.New(controlsupervisor.Config{
		InstanceID:      instanceID,
		SoftwareVersion: softwareVersion,
		GRPCEndpoint:    cfg.EngineGRPCAdvertise,
		EventJournal:    controlAdapter,
		JournalMetrics: func(ctx context.Context) (controlsupervisor.JournalPressure, error) {
			metrics, err := eventJournal.Metrics(ctx)
			if err != nil {
				return controlsupervisor.JournalPressure{}, err
			}
			return controlsupervisor.JournalPressure{
				State:   journalStateFor(metrics.State),
				Entries: positiveIntToUint64(metrics.Entries),
				Bytes:   positiveInt64ToUint64(metrics.Bytes),
			}, nil
		},
		StartedAt: time.Now(),
		Runtime:   controlRuntime,
	}, opener)
	if err != nil {
		return fmt.Errorf("build gateway control supervisor: %w", err)
	}
	applier := &desiredstate.ControlApplier{
		Reconciler: desiredReconciler,
		Health:     controlKeystore.Health,
		OnLeaseExpired: func(expireErr error) {
			if expireErr != nil {
				log.Warn("expire desired-state leases", "err", expireErr)
			}
			controlSupervisor.MarkDesiredStateUnhealthy()
			controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED)
			controlSupervisor.ReportNow()
		},
	}
	bootstrapApplier := &bootstrapControlApplier{
		delegate: applier,
		keystore: controlKeystore,
		onReport: func(state gatewayv1.GatewayRuntimeState) {
			controlRuntime.setState(state)
			controlSupervisor.ReportNow()
		},
	}
	controlSupervisor.SetDesiredState(bootstrapApplier)
	defer applier.Stop()

	// The control stream outlives the signal context so shutdown can report
	// DRAINING and DRAINED durably before transport cancellation.
	supervisorCtx, cancelSupervisor = context.WithCancel(context.Background())
	supervisorExited = make(chan struct{})
	certificateRenewalExpired = make(chan error, 1)
	go func() {
		renewalErr := renewGatewayCertificate(
			supervisorCtx,
			controlIdentity,
			cfg.CertificateRenewBefore,
			control,
			controlSupervisor,
			func() {
				controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED)
				reportControlRuntime(
					log,
					controlSupervisor,
					gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED,
					5*time.Second,
				)
			},
		)
		if renewalErr != nil {
			certificateRenewalExpired <- renewalErr
		}
	}()
	go func() {
		result := controlSupervisor.Run(supervisorCtx)
		supervisorResultMu.Lock()
		supervisorResult = result
		supervisorResultMu.Unlock()
		close(supervisorExited)
	}()
	supervisorStarted = true
	defer func() {
		cancelSupervisor()
		if supervisorStarted {
			<-supervisorExited
		}
		supervisorResultMu.Lock()
		result := supervisorResult
		supervisorResultMu.Unlock()
		if result != nil {
			log.Warn("gateway control supervisor stopped", "err", result)
		}
		if closeErr := control.Close(); closeErr != nil {
			log.Warn("close gateway control plane", "err", closeErr)
		}
	}()

	// --- Private engine gRPC listener ---
	engine := wa.NewApplicationGatewayAdapter(
		cfg.GatewayID,
		manager,
		desiredReconciler,
		newEngineDispatcher(waadapter.NewRoutingWAClient(manager)),
		journalCommandLedger{journal: eventJournal},
	)
	stopEngine, engineErr := startPrivateEngine(cfg.EngineGRPCAddr, cfg.GatewayID, controlIdentity, engine)
	if engineErr != nil {
		return fmt.Errorf("start private gateway engine: %w", engineErr)
	}
	defer stopEngine()

	var managerShutdownOnce sync.Once
	managerLifecycle := &managerLifecycleState{}
	shutdownManager := func() {
		managerShutdownOnce.Do(func() {
			managerLifecycle.markTerminal()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if shutdownErr := manager.Shutdown(shutdownCtx); shutdownErr != nil {
				log.Warn("shutdown session manager", "err", shutdownErr)
			}
			if closeErr := controlKeystore.Close(); closeErr != nil {
				log.Warn("checkpoint and close whatsmeow keystore", "err", closeErr)
			}
		})
	}
	defer shutdownManager()

	bootAllowed := true
	welcomePending := false
	welcomeResult := make(chan struct {
		status controlsupervisor.Status
		err    error
	}, 1)
	go func() {
		status, waitErr := controlSupervisor.WaitForWelcome(ctx)
		welcomeResult <- struct {
			status controlsupervisor.Status
			err    error
		}{status: status, err: waitErr}
	}()
	select {
	case result := <-welcomeResult:
		if result.err != nil {
			return fmt.Errorf("wait for gateway control welcome: %w", result.err)
		}
		bootAllowed = result.status.DesiredLifecycle == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN
		if bootAllowed {
			if _, desiredErr := controlSupervisor.WaitForDesiredState(ctx, result.status.ConnectionEpoch); desiredErr != nil {
				return fmt.Errorf("wait for gateway desired state: %w", desiredErr)
			}
		}
	case <-supervisorExited:
		supervisorResultMu.Lock()
		result := supervisorResult
		supervisorResultMu.Unlock()
		return fmt.Errorf("gateway control supervisor stopped before welcome: %w", result)
	case <-time.After(time.Second):
		// Keep the diagnostics listener available while the persistent
		// control connection reconnects. Admission remains closed.
		bootAllowed = false
		welcomePending = true
	}
	if !bootAllowed && !welcomePending {
		shutdownManager()
		controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED)
		controlSupervisor.ReportNow()
	}
	if welcomePending {
		go func() {
			status, waitErr := controlSupervisor.WaitForWelcome(supervisorCtx)
			if waitErr != nil {
				return
			}
			if status.DesiredLifecycle != gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN {
				shutdownManager()
				controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED)
				controlSupervisor.ReportNow()
				return
			}
			if _, desiredErr := controlSupervisor.WaitForDesiredState(supervisorCtx, status.ConnectionEpoch); desiredErr != nil {
				return
			}
			controlRuntime.setState(controlRuntimeStateForKeystore(controlKeystore))
			controlSupervisor.ReportNow()
		}()
	}

	// Desired-state snapshots own session startup. RUN reports readiness; DRAIN stops
	// sessions terminally and reports each transition before DISABLE exits.
	go func() {
		var afterSequence uint64
		for {
			directive, waitErr := controlSupervisor.WaitForDirective(supervisorCtx, afterSequence)
			if waitErr != nil {
				return
			}
			afterSequence = directive.Sequence
			if directive.Action == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN {
				runtimeState := controlRuntimeStateForKeystore(controlKeystore)
				failure := gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE
				if managerLifecycle.terminal() {
					runtimeState = gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED
					failure = gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_BUSY
				} else {
					controlRuntime.setState(runtimeState)
					controlSupervisor.ReportNow()
				}
				reportCtx, reportCancel := context.WithTimeout(context.Background(), 5*time.Second)
				if reportErr := controlSupervisor.ReportLifecycle(reportCtx, directive, runtimeState, failure); reportErr != nil {
					log.Warn("report lifecycle directive outcome", "directive", directive.ID, "err", reportErr)
				}
				reportCancel()
				continue
			}

			if managerLifecycle.terminal() {
				reportCtx, reportCancel := context.WithTimeout(context.Background(), 5*time.Second)
				if reportErr := controlSupervisor.ReportLifecycle(
					reportCtx,
					directive,
					gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED,
					gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE,
				); reportErr != nil {
					log.Warn("report lifecycle directive outcome", "directive", directive.ID, "err", reportErr)
				}
				reportCancel()
				if directive.Action == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DISABLE {
					select {
					case lifecycleDisabled <- struct{}{}:
					default:
					}
					return
				}
				continue
			}

			controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINING)
			reportControlRuntime(
				log,
				controlSupervisor,
				gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINING,
				5*time.Second,
			)
			shutdownManager()
			controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED)
			reportControlRuntime(
				log,
				controlSupervisor,
				gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED,
				5*time.Second,
			)
			reportCtx, reportCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if reportErr := controlSupervisor.ReportLifecycle(
				reportCtx,
				directive,
				gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED,
				gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE,
			); reportErr != nil {
				log.Warn("report lifecycle directive outcome", "directive", directive.ID, "err", reportErr)
			}
			reportCancel()
			if directive.Action == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DISABLE {
				select {
				case lifecycleDisabled <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	// The gateway serves no public API: every operation executes API-locally over
	// private engine RPCs. Only a minimal operational probe surface remains.
	mux := operationalHandler(readiness(controlSupervisor, eventJournal))
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	var terminalErr error
	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-supervisorExited:
		supervisorResultMu.Lock()
		result := supervisorResult
		supervisorResultMu.Unlock()
		return fmt.Errorf("gateway control supervisor stopped: %w", result)
	case renewalErr := <-certificateRenewalExpired:
		terminalErr = renewalErr
		log.Error("gateway certificate renewal ended", "err", renewalErr)
	case <-lifecycleDisabled:
		log.Info("gateway disabled by control-plane directive")
	case <-ctx.Done():
		log.Info("shutdown signal received, draining connections")
	}

	controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINING)
	reportControlRuntime(
		log,
		controlSupervisor,
		gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINING,
		5*time.Second,
	)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)
	shutdownManager()
	controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED)
	reportControlRuntime(
		log,
		controlSupervisor,
		gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED,
		5*time.Second,
	)
	if shutdownErr != nil {
		return fmt.Errorf("graceful shutdown: %w", shutdownErr)
	}
	if terminalErr != nil {
		return terminalErr
	}
	log.Info("gateway stopped cleanly")
	return nil
}

// managerLifecycleState tracks whether a DRAIN/DISABLE directive has terminally
// shut the manager down; RUN after that reports BUSY rather than rebooting.
type managerLifecycleState struct {
	mu           sync.Mutex
	terminalFlag bool
}

func (s *managerLifecycleState) markTerminal() {
	s.mu.Lock()
	s.terminalFlag = true
	s.mu.Unlock()
}

func (s *managerLifecycleState) terminal() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalFlag
}

// operationalHandler is the gateway's minimal net/http probe surface:
// unauthenticated /healthz and /readyz plus Prometheus /metrics. It exists so
// the composition root does not need any handler stack — the gateway has no
// public API surface.
func operationalHandler(readiness func() error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if readiness != nil {
			if err := readiness(); err != nil {
				httpx.WriteError(w, domain.ErrUnavailable("not ready: "+err.Error()))
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	mux.Handle("/metrics", promhttp.Handler())
	return mux
}

func reportControlRuntime(
	log *slog.Logger,
	supervisor *controlsupervisor.Supervisor,
	state gatewayv1.GatewayRuntimeState,
	timeout time.Duration,
) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := supervisor.Flush(ctx); err != nil {
		log.Warn("control runtime state was not acknowledged before shutdown", "state", state.String(), "err", err)
	}
}

// journalCommandLedger adapts the gateway event journal's command-result table
// to the engine adapter's transport-independent ledger port.
type journalCommandLedger struct{ journal *journal.Journal }

func (a journalCommandLedger) LookupCommand(
	ctx context.Context,
	commandID string,
) (*application.CommandResultRecord, error) {
	result, err := a.journal.LookupCommand(ctx, commandID)
	if err != nil || result == nil {
		return nil, err
	}
	return &application.CommandResultRecord{
		CommandID: result.CommandID, SessionID: result.SessionID, Status: result.Status,
		WAMessageID: result.WAMessageID, Error: result.Error, UpdatedAt: result.UpdatedAt,
	}, nil
}

func (a journalCommandLedger) SaveCommandResult(ctx context.Context, record application.CommandResultRecord) error {
	return a.journal.SaveCommandResult(ctx, journal.CommandResult{
		CommandID: record.CommandID, SessionID: record.SessionID, Status: record.Status,
		WAMessageID: record.WAMessageID, Error: record.Error, UpdatedAt: record.UpdatedAt,
	})
}

// journalStateFor maps the local journal capacity state onto the control-stream
// telemetry enum; unknown states report UNKNOWN rather than a fabricated one.
func journalStateFor(state journal.CapacityState) gatewayv1.GatewayJournalState {
	switch state {
	case journal.CapacityHealthy:
		return gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_HEALTHY
	case journal.CapacityDegraded:
		return gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_DEGRADED
	case journal.CapacityPaused:
		return gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_PAUSED
	case journal.CapacityCritical:
		return gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_CRITICAL
	default:
		return gatewayv1.GatewayJournalState_GATEWAY_JOURNAL_STATE_UNKNOWN
	}
}

func positiveIntToUint64(n int) uint64 {
	if n <= 0 {
		return 0
	}
	return uint64(n)
}

func positiveInt64ToUint64(n int64) uint64 {
	if n <= 0 {
		return 0
	}
	return uint64(n)
}

type controlStatusSource interface {
	Status() controlsupervisor.Status
}

type journalStatusSource interface {
	Metrics(context.Context) (journal.Metrics, error)
}

// readiness pings only gateway-local dependencies: the acknowledged control
// stream heartbeat and journal capacity. MySQL and Redis no longer exist here.
func readiness(control controlStatusSource, eventJournal journalStatusSource) func() error {
	return func() error {
		if control != nil {
			status := control.Status()
			if !status.Ready {
				if status.LastError != nil {
					return fmt.Errorf("control stream unavailable: %w", status.LastError)
				}
				return errors.New("control stream unavailable")
			}
		}
		if eventJournal != nil {
			metrics, err := eventJournal.Metrics(context.Background())
			if err != nil {
				return fmt.Errorf("gateway journal: %w", err)
			}
			if metrics.State == journal.CapacityCritical {
				return errors.New("gateway journal capacity critical")
			}
		}
		return nil
	}
}

type gatewayControlRuntime struct {
	mu    sync.RWMutex
	state gatewayv1.GatewayRuntimeState
	count func(context.Context) (int, error)
	log   *slog.Logger
}

func newGatewayControlRuntime(log *slog.Logger) *gatewayControlRuntime {
	return &gatewayControlRuntime{
		state: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_STARTING,
		log:   log,
	}
}

func (r *gatewayControlRuntime) setState(state gatewayv1.GatewayRuntimeState) {
	r.mu.Lock()
	r.state = state
	r.mu.Unlock()
}

func (r *gatewayControlRuntime) setSessionCounter(count func(context.Context) (int, error)) {
	r.mu.Lock()
	r.count = count
	r.mu.Unlock()
}

func (r *gatewayControlRuntime) Snapshot() controlsupervisor.RuntimeSnapshot {
	r.mu.RLock()
	state, count := r.state, r.count
	r.mu.RUnlock()
	if count == nil {
		return controlsupervisor.RuntimeSnapshot{State: state}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sessionCount, err := count(ctx)
	if err != nil {
		r.log.Warn("count gateway sessions for control heartbeat", "err", err)
		return controlsupervisor.RuntimeSnapshot{
			State: gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED,
		}
	}
	if sessionCount < 0 {
		sessionCount = 0
	}
	if sessionCount > int(^uint32(0)) {
		sessionCount = int(^uint32(0))
	}
	return controlsupervisor.RuntimeSnapshot{State: state, SessionCount: uint32(sessionCount)}
}

func controlRuntimeStateForKeystore(keystore *gatewayKeystoreRuntime) gatewayv1.GatewayRuntimeState {
	if keystore != nil {
		return keystore.RuntimeState()
	}
	return gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY
}

func newGatewayInstanceID() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(entropy[:]), nil
}

// setupLogging installs a JSON slog handler at the configured level.
func setupLogging(level string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	slog.SetDefault(slog.New(handler))
}
