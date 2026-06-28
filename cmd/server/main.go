// Command server is the gateway entrypoint and composition root: it loads
// configuration, opens the data stores, runs migrations, wires every subsystem
// (auth, keystore, outbound, stream, webhooks, the session manager, the async
// queue), builds the service layer + HTTP router, and runs an HTTP server with
// graceful shutdown.
//
// Subcommands:
//
//	server                 run the gateway (default)
//	server migrate up      apply all pending migrations
//	server migrate down    roll back one migration
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

	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/redis/go-redis/v9"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/assertion"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/config"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/crypto"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	gwhttp "github.com/ramaadi/quick-whatsapp-gateway/internal/http"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/handlers"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/queue"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/stream"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/inbound"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
	wastore "github.com/ramaadi/quick-whatsapp-gateway/internal/wa/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/webhooks"
	"github.com/ramaadi/quick-whatsapp-gateway/migrations"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := runMigrate(os.Args[2:]); err != nil {
			slog.Error("migrate failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
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

	// --- App data store (MySQL) + migrations ---
	if err := migrateUp(cfg.MySQLDSN); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	db, err := openMySQL(cfg.MySQLDSN)
	if err != nil {
		return fmt.Errorf("open mysql: %w", err)
	}
	defer db.Close()

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
	defer rdb.Close()

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
	_ = enqueuer // fan-out enqueue is invoked by the inbound pipeline (next stage).

	// --- Session manager (per-session whatsmeow clients) ---
	managerRepo := service.NewManagerSessionRepo(st.Sessions, nil)
	managerSink := service.NewEventSinkAdapter(publisher, log)
	manager := wa.NewManager(keystore, managerRepo, managerSink, nil, nil, log, wa.Config{
		AdminNumber:         cfg.WhatsAppAdminNumber,
		AdminOrganizationID: cfg.WhatsAppAdminOrgID,
		GatewayID:           cfg.GatewayID,
		DefaultRatePerMin:   cfg.DefaultRatePerMin,
		DefaultRatePerHour:  cfg.DefaultRatePerHour,
		DefaultAutoRead:     cfg.DefaultAutoRead,
	})
	inboundPipeline := inbound.NewPipeline(
		service.NewInboundNormalizer(),
		inbound.NewNoopCommandRegistry(),
		service.NewInboundRepos(st),
		publisher,
		service.NewInboundWebhookEnqueuerAdapter(enqueuer),
		manager.LiveOps(),
		inbound.SystemClock{},
		inbound.WithLogger(log),
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

	// Real send path: the Sender routes each request to the live per-session
	// whatsmeow client resolved from the manager (via outbound.WithSessionID on
	// the context). Sessions without a connected client return not_implemented.
	// Record every successful send into the messages table (from_me/out/sent) so
	// the gateway's own sends appear in chat history — whatsmeow never echoes a
	// self-authored send back to the inbound pipeline on the same device.
	msgRecorder := service.NewMessageRecorderAdapter(st.Messages, st.Chats, nil)
	sender := outbound.NewSender(service.NewRoutingWAClient(manager), outboxAdapter, limiter, outbound.SystemClock(),
		outbound.WithMessageRecorder(msgRecorder))

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

	// Background webhook dispatch loop.
	dispatchStop := startDispatchLoop(ctx, dispatcher, log)
	defer dispatchStop()

	// --- Services + handlers + router ---
	services := service.New(service.Deps{
		Store:                st,
		Manager:              manager,
		Sender:               sender,
		Crypto:               aes,
		DefaultRetryDelay:    cfg.WebhookRetryDelay,
		DefaultRetryAttempts: cfg.WebhookRetryAttempts,
		Log:                  log,
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
		// The router serves the public OpenAPI spec now (D9); the gateway does not.
		Log: log,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
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
	log.Info("server stopped cleanly")
	return nil
}

// registerGateway upserts this gateway's registry row with the given lifecycle
// status (§7, D8): id=GATEWAY_ID, base_url=PUBLIC_URL, timestamps = epoch-ms now.
// created_at is preserved on update by the repo; the heartbeat maintains
// last_seen_at + session_count thereafter.
func registerGateway(ctx context.Context, repo *store.GatewayRepo, cfg *config.Config, status domain.GatewayStatus) error {
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
func startGatewayHeartbeat(ctx context.Context, gateways *store.GatewayRepo, sessions *store.SessionRepo, cfg *config.Config, log *slog.Logger) func() {
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

func openMySQL(dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("MYSQL_DSN is required")
	}
	// Ensure parseTime is on so DATETIME columns round-trip as time.Time.
	dsn = ensureDSNParam(dsn, "parseTime", "parseTime=true")
	// Force CLIENT_FOUND_ROWS so RowsAffected() reports MATCHED rows, not CHANGED
	// rows. The store uses affectedOrNotFound on UPDATEs as an existence assertion
	// (e.g. mark-chat-read, PATCH chat flags); without this, an idempotent update
	// that sets a row to the values it already holds reports 0 affected and is
	// misread as a 404. clientFoundRows makes "matched a row" the success signal.
	dsn = ensureDSNParam(dsn, "clientFoundRows", "clientFoundRows=true")
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// ensureDSNParam appends `kv` (e.g. "parseTime=true") to a MySQL DSN's query
// string unless the named param is already present, picking the right `?`/`&`
// separator. It leaves an explicitly-set value untouched so a deployment can
// still override.
func ensureDSNParam(dsn, name, kv string) string {
	if strings.Contains(dsn, name+"=") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + kv
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

// migrator builds a *migrate.Migrate over the embedded migrations and the DB.
func migrator(db *sql.DB) (*migrate.Migrate, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("open migration source: %w", err)
	}
	driver, err := migratemysql.WithInstance(db, &migratemysql.Config{})
	if err != nil {
		return nil, fmt.Errorf("migrate driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "mysql", driver)
	if err != nil {
		return nil, fmt.Errorf("migrate instance: %w", err)
	}
	return m, nil
}

func migrateUp(dsn string) error {
	db, err := openMigrateMySQL(dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	m, err := migrator(db)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// openMigrateMySQL opens a MySQL connection for running migrations. golang-migrate's
// mysql WithInstance driver runs each migration file as a single Exec, so a file
// with more than one statement (e.g. an ALTER plus a CREATE INDEX, or several
// CREATE TABLEs) needs multiStatements enabled — otherwise MySQL rejects the
// second statement with a 1064 syntax error. The app's own pool deliberately
// leaves this off; only the migrator needs it.
func openMigrateMySQL(dsn string) (*sql.DB, error) {
	return openMySQL(ensureDSNParam(dsn, "multiStatements", "multiStatements=true"))
}

// runMigrate implements `migrate up|down`.
func runMigrate(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	setupLogging(cfg.LogLevel)
	db, err := openMigrateMySQL(cfg.MySQLDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	m, err := migrator(db)
	if err != nil {
		return err
	}
	direction := "up"
	if len(args) > 0 {
		direction = args[0]
	}
	switch direction {
	case "up":
		if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return err
		}
		fmt.Println("migrations applied")
	case "down":
		if err := m.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return err
		}
		fmt.Println("rolled back one migration")
	default:
		return fmt.Errorf("unknown migrate direction %q (want up|down)", direction)
	}
	return nil
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
