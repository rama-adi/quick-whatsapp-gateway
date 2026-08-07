// Command gateway is the gateway entrypoint and composition root: it loads
// configuration, opens the data stores, wires every subsystem
// (auth, keystore, outbound, stream, webhooks, the session manager, the async
// queue), builds the service layer + HTTP router, and runs an HTTP server with
// graceful shutdown.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/redis/go-redis/v9"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/assertion"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/config"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/crypto"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/dbconn"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/gateway/controlclient"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/gateway/controlsupervisor"
	gwhttp "github.com/ramaadi/quick-whatsapp-gateway/internal/http"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/handlers"
	httpmiddleware "github.com/ramaadi/quick-whatsapp-gateway/internal/http/middleware"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/oidp"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki/gatewayidentity"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/queue"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/stream"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/inbound"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
	wastore "github.com/ramaadi/quick-whatsapp-gateway/internal/wa/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/webhooks"
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

	// Optional private control-plane bootstrap is fail-closed. With no target this
	// path performs no filesystem access and the legacy runtime is unchanged.
	controlEnabled := cfg.ControlPlaneAddr != ""
	admissionGate := gwhttp.NewAdmissionGate(!controlEnabled)
	var control *controlclient.Client
	var controlSupervisor *controlsupervisor.Supervisor
	var controlRuntime *gatewayControlRuntime
	var supervisorCtx context.Context
	var supervisorExited chan struct{}
	var supervisorResultMu sync.Mutex
	var supervisorResult error
	lifecycleDisabled := make(chan struct{}, 1)
	if controlEnabled {
		caInfo, statErr := os.Stat(cfg.BootstrapCAFile)
		if statErr != nil {
			return fmt.Errorf("stat gateway bootstrap CA: %w", statErr)
		}
		if !caInfo.Mode().IsRegular() || caInfo.Size() <= 0 || caInfo.Size() > 16<<10 {
			return fmt.Errorf("gateway bootstrap CA must be a regular file within 16 KiB")
		}
		bootstrapCA, readErr := os.ReadFile(cfg.BootstrapCAFile)
		if readErr != nil {
			return fmt.Errorf("read gateway bootstrap CA: %w", readErr)
		}
		identity, identityErr := gatewayidentity.New(gatewayidentity.Config{Directory: cfg.CredentialDir, GatewayID: cfg.GatewayID, BootstrapCA: bootstrapCA})
		if identityErr != nil {
			return fmt.Errorf("build gateway identity: %w", identityErr)
		}
		control, err = controlclient.New(controlclient.Config{Target: cfg.ControlPlaneAddr, GatewayID: cfg.GatewayID, Identity: identity})
		if err != nil {
			return fmt.Errorf("build gateway control client: %w", err)
		}
		if err = control.Ensure(ctx, cfg.EnrollmentToken); err != nil {
			return fmt.Errorf("connect gateway control plane: %w", err)
		}
		cfg.EnrollmentToken = ""
		_ = os.Unsetenv("GATEWAY_ENROLLMENT_TOKEN")
		opener, openerErr := controlsupervisor.NewConnOpener(control.Conn())
		if openerErr != nil {
			_ = control.Close()
			return fmt.Errorf("build gateway control stream: %w", openerErr)
		}
		instanceID, instanceErr := newGatewayInstanceID()
		if instanceErr != nil {
			_ = control.Close()
			return fmt.Errorf("create gateway process instance id: %w", instanceErr)
		}
		controlRuntime = newGatewayControlRuntime(log)
		controlSupervisor, err = controlsupervisor.New(controlsupervisor.Config{
			InstanceID:      instanceID,
			SoftwareVersion: softwareVersion,
			HTTPBaseURL:     cfg.PublicURL,
			StartedAt:       time.Now(),
			Runtime:         controlRuntime,
		}, opener)
		if err != nil {
			_ = control.Close()
			return fmt.Errorf("build gateway control supervisor: %w", err)
		}
		// The control stream outlives the signal context so shutdown can report
		// DRAINING and DRAINED durably before transport cancellation.
		var cancelSupervisor context.CancelFunc
		supervisorCtx, cancelSupervisor = context.WithCancel(context.Background())
		supervisorExited = make(chan struct{})
		go func() {
			result := controlSupervisor.Run(supervisorCtx)
			supervisorResultMu.Lock()
			supervisorResult = result
			supervisorResultMu.Unlock()
			close(supervisorExited)
		}()
		go func() {
			status := controlSupervisor.Status()
			for {
				admissionGate.SetOpen(status.Ready)
				next, waitErr := controlSupervisor.WaitForStatusChange(supervisorCtx, status)
				if waitErr != nil {
					return
				}
				status = next
			}
		}()
		defer func() {
			cancelSupervisor()
			<-supervisorExited
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
	}

	// --- Transitional app-data store (schema migration is owned by the API) ---
	db, err := dbconn.OpenMySQL(cfg.MySQLDSN)
	if err != nil {
		return fmt.Errorf("open mysql: %w", err)
	}
	defer func() { _ = db.Close() }()
	prometheus.MustRegister(collectors.NewDBStatsCollector(db, "gateway"))

	st := store.New(db)
	if controlRuntime != nil {
		controlRuntime.setSessionCounter(func(ctx context.Context) (int, error) {
			return st.Sessions.CountByGateway(ctx, cfg.GatewayID)
		})
	}

	// --- Crypto (AES-GCM for secrets at rest) ---
	aes, err := crypto.NewAESGCM(cfg.AppEncryptionKey)
	if err != nil {
		return fmt.Errorf("init crypto: %w", err)
	}

	// --- Redis ---
	rdb, err := openRedis(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("open redis: %w", err)
	}
	defer func() { _ = rdb.Close() }()

	// --- Trust seam (§4, D2/D3): the gateway no longer authenticates end users.
	// The central router terminates authn and vouches a resolved Principal via a
	// short-lived, request-bound Ed25519 assertion; the gateway verifies it against
	// the router's JWKS (ROUTER_JWKS_URL) and rebuilds the Principal from it.
	if cfg.RouterJWKSURL == "" {
		return fmt.Errorf("ROUTER_JWKS_URL is required: the gateway verifies the router's internal assertion")
	}
	routerKeys, err := assertion.NewRemoteKeySet(cfg.RouterJWKSURL)
	if err != nil {
		return fmt.Errorf("build router jwks source: %w", err)
	}
	assertionVerifier, err := assertion.NewVerifier(routerKeys, cfg.RouterAssertionIssuer, cfg.GatewayID)
	if err != nil {
		return fmt.Errorf("build assertion verifier: %w", err)
	}

	// --- whatsmeow keystore (gateway-local SQLite, §6.1) ---
	keystore, err := wastore.Open(ctx, cfg.WhatsmeowStoreDSN, nil)
	if err != nil {
		return fmt.Errorf("open whatsmeow keystore: %w", err)
	}

	// --- Stream publisher (event fan-out over Redis pub/sub) ---
	publisher := stream.NewPublisher(rdb, log)

	// --- Outbound pipeline (rate limiter + sender over the outbox) ---
	// The Sender is account-global; its WAClient is constructed below once the
	// session manager exists, so it can route each send to the per-session
	// whatsmeow client (outbound.RoutingWAClient resolves it from the manager).
	limiter := outbound.NewRedisRateLimiter(rdb)
	outboxAdapter := service.NewOutboxRepoAdapter(st.Outbox, nil)

	// --- Webhooks (enqueuer + dispatcher) ---
	whRepo := service.NewWebhookRepoAdapter(st.Webhooks)
	whDeliveries := service.NewWebhookDeliveryRepoAdapter(st.WebhookDeliveries)
	enqueuer := webhooks.NewEnqueuer(whRepo, whDeliveries, nil, log)
	dispatcher := webhooks.NewDispatcher(whRepo, whDeliveries, st.EventLog, &http.Client{Timeout: 30 * time.Second}, aes, nil, log)
	pollRecaps := service.NewPollRecapWorker(st, publisher, enqueuer, rdb, service.PollRecapConfig{
		RedisPrefix: cfg.RedisPrefix,
		Log:         log,
	})
	pollRecapStop := pollRecaps.Start(ctx)
	defer pollRecapStop()

	// --- Session manager (per-session whatsmeow clients) ---
	managerRepo := service.NewManagerSessionRepo(st.Sessions, nil)
	managerSink := service.NewEventSinkAdapter(publisher, log)
	manager := wa.NewManager(keystore, managerRepo, managerSink, nil, nil, log, wa.Config{
		AdminNumber:         cfg.WhatsAppAdminNumber,
		AdminOrganizationID: cfg.WhatsAppAdminOrgID,
		GatewayID:           cfg.GatewayID,
		DeviceName:          cfg.WhatsAppDeviceName,
		DefaultRatePerMin:   cfg.DefaultRatePerMin,
		DefaultRatePerHour:  cfg.DefaultRatePerHour,
		DefaultAutoRead:     cfg.DefaultAutoRead,
	})
	msgRecorder := service.NewMessageRecorderAdapter(st.Messages, st.Chats, st.Polls, pollRecaps, nil)
	sender := outbound.NewSender(service.NewRoutingWAClient(manager), outboxAdapter, limiter, outbound.SystemClock(),
		outbound.WithMessageRecorder(msgRecorder),
		outbound.WithQuoteResolver(st.Messages))
	pending := oidp.NewPendingStore(rdb, cfg.RedisPrefix, 10*time.Minute)
	loginInterceptor := oidp.NewLoginInterceptor(
		st.OAuthClients,
		pending,
		service.NewOIDPGroupMemberChecker(st.GroupMembers),
		service.NewOIDPBotFeedback(st.Sessions, sender),
		log,
	)
	oidpAppChanges := oidp.NewAppChangeSubscriber(rdb, loginInterceptor, log)
	if err := oidpAppChanges.Start(ctx); err != nil {
		log.Warn("oidp app control-bus subscriber disabled", "err", err)
	} else {
		defer oidpAppChanges.Stop()
	}
	inboundPipeline := inbound.NewPipeline(
		service.NewInboundNormalizer(manager.LiveOps(), st.Polls),
		inbound.NewNoopCommandRegistry(),
		service.NewInboundRepos(st, pollRecaps),
		publisher,
		service.NewInboundWebhookEnqueuerAdapter(enqueuer),
		manager.LiveOps(),
		inbound.SystemClock{},
		inbound.WithLogger(log),
		inbound.WithLoginInterceptor(loginInterceptor),
		inbound.WithSessionConfig(func(sessionID string) (inbound.SessionConfig, bool) {
			s, err := st.Sessions.Get(context.Background(), sessionID)
			if err != nil {
				return inbound.SessionConfig{}, false
			}
			return inbound.SessionConfig{
				AutoRead:       s.AutoRead,
				PresenceTyping: s.PresenceTyping,
			}, true
		}),
	)
	inboundHandler := service.NewInboundPipelineHandler(inboundPipeline, log)
	manager.SetInboundHandler(inboundHandler)
	// Boot orphan-guard (§4.6 boot reconciliation, §17 R2): before resuming a
	// session, confirm its owning org still exists in better-auth's shared
	// `organization` table; orphaned sessions are marked STOPPED and not resumed.
	orgReader := store.NewOrganizationReader(db)
	manager.SetOrgExists(orgReader.Exists)

	// Registry lifecycle (D8). Register as `joining` before the manager adopts
	// sessions, flip to `active` once boot succeeds, then heartbeat last_seen_at +
	// session_count on a timer so the router can route by liveness and load.
	// Best-effort: a registry write failure is logged, not fatal.
	if !controlEnabled {
		if err := registerGateway(ctx, st.Gateways, cfg, domain.GatewayJoining); err != nil {
			log.Error("register gateway (joining) failed", "gateway", cfg.GatewayID, "err", err)
		}
	}

	var managerShutdownOnce sync.Once
	var managerLifecycleMu sync.Mutex
	managerTerminal := false
	managerBooted := false
	bootManager := func(bootCtx context.Context) (string, error) {
		managerLifecycleMu.Lock()
		defer managerLifecycleMu.Unlock()
		if managerTerminal || managerBooted {
			return "", nil
		}
		adminCode, bootErr := manager.Boot(bootCtx)
		if bootErr == nil {
			managerBooted = true
		}
		return adminCode, bootErr
	}
	managerState := func() (terminal, booted bool) {
		managerLifecycleMu.Lock()
		defer managerLifecycleMu.Unlock()
		return managerTerminal, managerBooted
	}
	shutdownManager := func() {
		managerShutdownOnce.Do(func() {
			managerLifecycleMu.Lock()
			defer managerLifecycleMu.Unlock()
			managerTerminal = true
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if shutdownErr := manager.Shutdown(shutdownCtx); shutdownErr != nil {
				log.Warn("shutdown session manager", "err", shutdownErr)
			}
		})
	}
	defer shutdownManager()

	bootAllowed := true
	welcomePending := false
	if controlSupervisor != nil {
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
	}
	var adminCode string
	if bootAllowed {
		adminCode, err = bootManager(ctx)
	} else if !welcomePending {
		shutdownManager()
		controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED)
		controlSupervisor.ReportNow()
	}
	if err != nil {
		// Non-fatal: the HTTP surface should still come up so sessions can be
		// (re)attached via the API.
		log.Error("session manager boot failed", "err", err)
		if controlRuntime != nil {
			controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED)
		}
	} else if controlRuntime != nil && bootAllowed {
		controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY)
		controlSupervisor.ReportNow()
	}
	if adminCode != "" {
		log.Info("admin session pairing code", "code", adminCode, "number", cfg.WhatsAppAdminNumber)
		fmt.Printf("\n=== WhatsApp admin pairing code: %s (number %s) ===\n\n", adminCode, cfg.WhatsAppAdminNumber)
	}

	// Now reachable and adopting sessions → mark active and start heartbeating.
	var heartbeatStop func()
	if !controlEnabled {
		if err := registerGateway(ctx, st.Gateways, cfg, domain.GatewayActive); err != nil {
			log.Error("register gateway (active) failed", "gateway", cfg.GatewayID, "err", err)
		}
		heartbeatStop = startGatewayHeartbeat(ctx, st.Gateways, st.Sessions, cfg, log)
		defer heartbeatStop()
	}
	// Graceful drain on shutdown: stop taking new placements (draining), then mark
	// drained once the process is on its way out, so the router stops routing here.
	if !controlEnabled {
		defer func() {
			drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := st.Gateways.SetStatus(drainCtx, cfg.GatewayID, domain.GatewayDrained, domain.NowMs()); err != nil {
				log.Warn("mark gateway drained failed", "err", err)
			}
		}()
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
			adminCode, bootErr := bootManager(supervisorCtx)
			if bootErr != nil {
				log.Error("deferred session manager boot failed", "err", bootErr)
				controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED)
				return
			}
			if adminCode != "" {
				log.Info("admin session pairing code", "code", adminCode, "number", cfg.WhatsAppAdminNumber)
				fmt.Printf("\n=== WhatsApp admin pairing code: %s (number %s) ===\n\n", adminCode, cfg.WhatsAppAdminNumber)
			}
			controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY)
			controlSupervisor.ReportNow()
		}()
	}
	// --- Async queue (asynq workers: outbox + retention) ---
	redisOpt, err := queue.ParseRedisURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("parse redis url for queue: %w", err)
	}
	qHandlers := queue.Handlers{
		Outbox:    service.NewOutboxWorker(st.Outbox, sender, log),
		Retention: service.NewRetentionWorker(st, log),
		// Per-task webhook delivery lands in the next stage; the dispatcher's
		// DeliverDue ticker (below) drives delivery today.
	}
	qServer := queue.NewServer(redisOpt, queue.ServerConfig{}, qHandlers)
	workers := newAdmittedWorkers(qServer, func() bool {
		return !controlEnabled || controlSupervisor.Status().Ready
	})
	startWorkers := func() error {
		return workers.Start()
	}
	stopWorkers := func() {
		workers.Drain()
	}
	if !controlEnabled {
		if err := startWorkers(); err != nil {
			return fmt.Errorf("start queue server: %w", err)
		}
	} else {
		go func() {
			status := controlSupervisor.Status()
			for !status.Ready {
				next, waitErr := controlSupervisor.WaitForStatusChange(supervisorCtx, status)
				if waitErr != nil {
					return
				}
				status = next
			}
			if startErr := startWorkers(); startErr != nil {
				log.Error("start admitted queue workers", "err", startErr)
			}
		}()
	}
	defer stopWorkers()
	if controlSupervisor != nil {
		go func() {
			var afterSequence uint64
			for {
				directive, waitErr := controlSupervisor.WaitForDirective(supervisorCtx, afterSequence)
				if waitErr != nil {
					return
				}
				afterSequence = directive.Sequence
				if directive.Action == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_RUN {
					runtimeState := gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY
					failure := gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE
					terminal, _ := managerState()
					if terminal {
						runtimeState = gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED
						failure = gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_BUSY
					} else {
						adminCode, bootErr := bootManager(supervisorCtx)
						if bootErr != nil {
							log.Error("directive session manager boot failed", "err", bootErr)
							runtimeState = gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED
							failure = gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_INTERNAL
						} else if adminCode != "" {
							log.Info("admin session pairing code", "code", adminCode, "number", cfg.WhatsAppAdminNumber)
							fmt.Printf("\n=== WhatsApp admin pairing code: %s (number %s) ===\n\n", adminCode, cfg.WhatsAppAdminNumber)
						}
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

				terminal, _ := managerState()
				if terminal {
					reportCtx, reportCancel := context.WithTimeout(context.Background(), 5*time.Second)
					if reportErr := controlSupervisor.ReportLifecycle(reportCtx, directive, gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED, gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE); reportErr != nil {
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

				drainCtx := context.Background()
				var drainCancel context.CancelFunc
				if directive.DrainDeadline.IsZero() {
					drainCtx, drainCancel = context.WithTimeout(drainCtx, 10*time.Second)
				} else {
					drainCtx, drainCancel = context.WithDeadline(drainCtx, directive.DrainDeadline)
				}
				failure := gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_NONE
				if drainErr := admissionGate.CloseAndWait(drainCtx); drainErr != nil {
					log.Warn("wait for admitted gateway requests", "err", drainErr)
					if errors.Is(drainErr, context.DeadlineExceeded) {
						failure = gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_TIMEOUT
					} else {
						failure = gatewayv1.LifecycleFailure_LIFECYCLE_FAILURE_INTERNAL
					}
				}
				drainCancel()
				stopWorkers()
				controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINING)
				reportControlRuntime(log, controlSupervisor, gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINING, 5*time.Second)
				shutdownManager()
				controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED)
				reportControlRuntime(log, controlSupervisor, gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED, 5*time.Second)
				reportCtx, reportCancel := context.WithTimeout(context.Background(), 5*time.Second)
				if reportErr := controlSupervisor.ReportLifecycle(reportCtx, directive, gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED, failure); reportErr != nil {
					log.Warn("report lifecycle directive outcome", "directive", directive.ID, "err", reportErr)
				}
				reportCancel()
				if directive.Action == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DISABLE {
					select {
					case lifecycleDisabled <- struct{}{}:
					default:
					}
				}
				if directive.Action == gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DISABLE {
					return
				}
			}
		}()
	}
	qClient := queue.NewClient(redisOpt)
	defer func() {
		if err := qClient.Close(); err != nil {
			log.Warn("close queue client", "err", err)
		}
	}()
	retentionStop := queue.NewRetentionScheduler(rdb, qClient, queue.RetentionSchedulerConfig{
		RetentionDays: cfg.RetentionDays,
		RedisPrefix:   cfg.RedisPrefix,
		Log:           log,
	}).Start(ctx)
	defer retentionStop()

	// Background webhook dispatch loop.
	dispatchStop := startDispatchLoop(ctx, dispatcher, log)
	defer dispatchStop()

	// --- Services + handlers + router ---
	services := service.New(service.Deps{
		Store:                      st,
		Manager:                    manager,
		Sender:                     sender,
		Crypto:                     aes,
		OAuthClientSecretPepper:    os.Getenv("OAUTH_CLIENT_SECRET_PEPPER"),
		WhatsAppAdminCommandPrefix: cfg.WhatsAppAdminCmdPrefix,
		ControlPublisher:           service.NewRedisControlPublisher(rdb),
		DefaultRetryDelay:          cfg.WebhookRetryDelay,
		DefaultRetryAttempts:       cfg.WebhookRetryAttempts,
		Log:                        log,
	})

	// Realtime is WebSocket-only and lives on the central router, which owns the
	// ticket+WS endpoint and subscribes to the events the gateway publishes to
	// Redis (above). The gateway no longer serves any client transport.
	h := handlers.New(services, log)
	router := gwhttp.NewRouter(gwhttp.RouterConfig{
		Handlers:  h,
		Auth:      assertion.Middleware(assertionVerifier),
		Limiter:   nil, // HTTP-edge rate limiting optional; outbound limits sends.
		Readiness: readiness(db, rdb, controlSupervisor),
		Admission: admissionGate,
		DBStats:   db.Stats,
		SessionState: func(sessionID string) (httpmiddleware.SessionState, bool) {
			status, connected, loggedIn, ok := manager.ConnectionState(sessionID)
			return httpmiddleware.SessionState{
				Status:    string(status),
				Connected: connected,
				LoggedIn:  loggedIn,
			}, ok
		},
		// The router serves the public OpenAPI spec now (D9); the gateway does not.
		Log: log,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout is a socket-level backstop above the per-request context
		// deadline (middleware.Timeout, ~15s): the deadline cancels a wedged DB query
		// and returns a 503 first; this only trips if a handler somehow blocks past
		// it, guaranteeing the connection is never held open forever. The gateway
		// serves only unary JSON (realtime/streaming lives on the router), so a write
		// deadline is safe here.
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-supervisorExited:
		supervisorResultMu.Lock()
		result := supervisorResult
		supervisorResultMu.Unlock()
		return fmt.Errorf("gateway control supervisor stopped: %w", result)
	case <-lifecycleDisabled:
		log.Info("gateway disabled by control-plane directive")
	case <-ctx.Done():
		log.Info("shutdown signal received, draining connections")
	}

	// Mark draining so the router stops placing new sessions here while in-flight
	// work finishes (the deferred drained transition runs after Shutdown returns).
	admissionDrainCtx, admissionDrainCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := admissionGate.CloseAndWait(admissionDrainCtx); err != nil {
		log.Warn("wait for admitted gateway requests", "err", err)
	}
	admissionDrainCancel()
	stopWorkers()
	if controlRuntime != nil {
		controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINING)
		reportControlRuntime(log, controlSupervisor, gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINING, 5*time.Second)
	}
	if !controlEnabled {
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := st.Gateways.SetStatus(drainCtx, cfg.GatewayID, domain.GatewayDraining, domain.NowMs()); err != nil {
			log.Warn("mark gateway draining failed", "err", err)
		}
		drainCancel()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)
	shutdownManager()
	if controlRuntime != nil {
		controlRuntime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED)
		reportControlRuntime(log, controlSupervisor, gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DRAINED, 5*time.Second)
	}
	if shutdownErr != nil {
		return fmt.Errorf("graceful shutdown: %w", shutdownErr)
	}
	log.Info("gateway stopped cleanly")
	return nil
}

func reportControlRuntime(log *slog.Logger, supervisor *controlsupervisor.Supervisor, state gatewayv1.GatewayRuntimeState, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := supervisor.Flush(ctx); err != nil {
		log.Warn("control runtime state was not acknowledged before shutdown", "state", state.String(), "err", err)
	}
}

// registerGateway upserts this gateway's registry row with the given lifecycle
// status (§7, D8): id=GATEWAY_ID, base_url=GATEWAY_PUBLIC_URL (or legacy
// PUBLIC_URL), timestamps = epoch-ms now.
// created_at is preserved on update by the repo; the heartbeat maintains
// last_seen_at + session_count thereafter.
func registerGateway(ctx context.Context, repo *store.GatewayRepo, cfg *config.GatewayConfig, status domain.GatewayStatus) error {
	now := domain.NowMs()
	g := domain.Gateway{
		ID:         cfg.GatewayID,
		Status:     status,
		LastSeenAt: &now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if cfg.PublicURL != "" {
		base := cfg.PublicURL
		g.BaseURL = &base
	}
	return repo.Upsert(ctx, g)
}

// startGatewayHeartbeat refreshes last_seen_at + session_count on a timer so the
// router can prune stale gateways and place new sessions on the least-loaded one
// (D8). It returns a stop func for the shutdown sequence.
func startGatewayHeartbeat(ctx context.Context, gateways *store.GatewayRepo, sessions *store.SessionRepo, cfg *config.GatewayConfig, log *slog.Logger) func() {
	loopCtx, cancel := context.WithCancel(ctx)
	beat := func() {
		count, err := sessions.CountByGateway(loopCtx, cfg.GatewayID)
		if err != nil {
			log.Warn("gateway heartbeat: count sessions failed", "err", err)
			count = 0
		}
		if err := gateways.Heartbeat(loopCtx, cfg.GatewayID, domain.NowMs(), count); err != nil {
			log.Warn("gateway heartbeat failed", "err", err)
		}
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		beat() // beat once immediately so load is current right after boot
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				beat()
			}
		}
	}()
	return cancel
}

// startDispatchLoop runs the webhook dispatcher on a ticker until ctx is done.
func startDispatchLoop(ctx context.Context, d *webhooks.Dispatcher, log *slog.Logger) func() {
	loopCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				if _, err := d.DeliverDue(loopCtx, webhooks.DefaultClaimLimit); err != nil {
					log.WarnContext(loopCtx, "webhook dispatch pass failed", "err", err)
				}
			}
		}
	}()
	return cancel
}

// readiness returns a /readyz probe that pings the DB and Redis.
type controlStatusSource interface {
	Status() controlsupervisor.Status
}

func readiness(db *sql.DB, rdb *redis.Client, control controlStatusSource) func() error {
	return func() error {
		if control != nil && !control.Status().Ready {
			return errors.New("control stream unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			return fmt.Errorf("mysql: %w", err)
		}
		if err := rdb.Ping(ctx).Err(); err != nil {
			return fmt.Errorf("redis: %w", err)
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

func newGatewayInstanceID() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(entropy[:]), nil
}

type workerServer interface {
	Start() error
	Shutdown()
}

type admittedWorkers struct {
	mu       sync.Mutex
	server   workerServer
	ready    func() bool
	started  bool
	terminal bool
}

func newAdmittedWorkers(server workerServer, ready func() bool) *admittedWorkers {
	return &admittedWorkers{server: server, ready: ready}
}

func (w *admittedWorkers) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.terminal || w.started || (w.ready != nil && !w.ready()) {
		return nil
	}
	if err := w.server.Start(); err != nil {
		return err
	}
	w.started = true
	return nil
}

func (w *admittedWorkers) Drain() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.terminal = true
	if w.started {
		w.server.Shutdown()
		w.started = false
	}
}

func openRedis(rawURL string) (*redis.Client, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("REDIS_URL is required")
	}
	opt, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	return redis.NewClient(opt), nil
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
