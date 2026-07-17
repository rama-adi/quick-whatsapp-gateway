// Command gateway is the gateway entrypoint and composition root: it loads
// configuration, opens the data stores, wires every subsystem
// (auth, keystore, outbound, stream, webhooks, the session manager, the async
// queue), builds the service layer + HTTP router, and runs an HTTP server with
// graceful shutdown.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/redis/go-redis/v9"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/assertion"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/config"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/crypto"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/dbconn"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	gwhttp "github.com/ramaadi/quick-whatsapp-gateway/internal/http"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/handlers"
	httpmiddleware "github.com/ramaadi/quick-whatsapp-gateway/internal/http/middleware"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/oidp"
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

	// --- Transitional app-data store (schema migration is owned by the API) ---
	db, err := dbconn.OpenMySQL(cfg.MySQLDSN)
	if err != nil {
		return fmt.Errorf("open mysql: %w", err)
	}
	defer func() { _ = db.Close() }()
	prometheus.MustRegister(collectors.NewDBStatsCollector(db, "gateway"))

	st := store.New(db)

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
	if err := registerGateway(ctx, st.Gateways, cfg, domain.GatewayJoining); err != nil {
		log.Error("register gateway (joining) failed", "gateway", cfg.GatewayID, "err", err)
	}

	adminCode, err := manager.Boot(ctx)
	if err != nil {
		// Non-fatal: the HTTP surface should still come up so sessions can be
		// (re)attached via the API.
		log.Error("session manager boot failed", "err", err)
	}
	if adminCode != "" {
		log.Info("admin session pairing code", "code", adminCode, "number", cfg.WhatsAppAdminNumber)
		fmt.Printf("\n=== WhatsApp admin pairing code: %s (number %s) ===\n\n", adminCode, cfg.WhatsAppAdminNumber)
	}

	// Now reachable and adopting sessions → mark active and start heartbeating.
	if err := registerGateway(ctx, st.Gateways, cfg, domain.GatewayActive); err != nil {
		log.Error("register gateway (active) failed", "gateway", cfg.GatewayID, "err", err)
	}
	heartbeatStop := startGatewayHeartbeat(ctx, st.Gateways, st.Sessions, cfg, log)
	defer heartbeatStop()
	// Graceful drain on shutdown: stop taking new placements (draining), then mark
	// drained once the process is on its way out, so the router stops routing here.
	defer func() {
		drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := st.Gateways.SetStatus(drainCtx, cfg.GatewayID, domain.GatewayDrained, domain.NowMs()); err != nil {
			log.Warn("mark gateway drained failed", "err", err)
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = manager.Shutdown(shutdownCtx)
	}()

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
	if err := qServer.Start(); err != nil {
		return fmt.Errorf("start queue server: %w", err)
	}
	defer qServer.Shutdown()
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
		Readiness: readiness(db, rdb),
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
	case <-ctx.Done():
		log.Info("shutdown signal received, draining connections")
	}

	// Mark draining so the router stops placing new sessions here while in-flight
	// work finishes (the deferred drained transition runs after Shutdown returns).
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := st.Gateways.SetStatus(drainCtx, cfg.GatewayID, domain.GatewayDraining, domain.NowMs()); err != nil {
		log.Warn("mark gateway draining failed", "err", err)
	}
	drainCancel()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("gateway stopped cleanly")
	return nil
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
func readiness(db *sql.DB, rdb *redis.Client) func() error {
	return func() error {
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
