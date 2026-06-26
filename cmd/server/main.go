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

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/config"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/controlbus"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/crypto"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	gwhttp "github.com/ramaadi/quick-whatsapp-gateway/internal/http"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/handlers"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/queue"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/stream"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa"
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
	db, err := openMySQL(cfg.MySQLDSN)
	if err != nil {
		return fmt.Errorf("open mysql: %w", err)
	}
	defer db.Close()

	if err := migrateUp(db); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

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

	// --- Trust model (§4): the gateway VERIFIES callers, it does not log them in.
	// JWTs (humans) verify locally against the better-auth JWKS; api-keys (machines)
	// verify against the shared MySQL `apikey` table via the pinned hash scheme.
	tokenVerifier, err := authz.NewJWTVerifier(cfg.BetterAuthJWKSURL, cfg.BetterAuthURL)
	if err != nil {
		return fmt.Errorf("build jwt verifier: %w", err)
	}
	baseKeyVerifier, err := authz.NewAPIKeyVerifier(st.APIKeys, authz.DefaultHasher())
	if err != nil {
		return fmt.Errorf("build api-key verifier: %w", err)
	}
	// Positive cache in front of the api-key verifier (§4.6): a busy client costs
	// at most one MySQL lookup per ~60s, and the control bus evicts on revocation.
	// FAIL-CLOSED — only successes are cached, and the TTL is the backstop.
	keyVerifier := authz.NewCachingKeyVerifier(baseKeyVerifier, authz.DefaultKeyCacheTTL)

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
	inboundHandler := service.NewInboundLogHandler(log)
	manager := wa.NewManager(keystore, managerRepo, managerSink, inboundHandler, nil, log, wa.Config{
		AdminNumber:        cfg.WhatsAppAdminNumber,
		GatewayID:          cfg.GatewayID,
		DefaultRatePerMin:  cfg.DefaultRatePerMin,
		DefaultRatePerHour: cfg.DefaultRatePerHour,
		DefaultAutoRead:    cfg.DefaultAutoRead,
	})
	// Boot orphan-guard (§4.6 boot reconciliation, §17 R2): before resuming a
	// session, confirm its owning org still exists in better-auth's shared
	// `organization` table; orphaned sessions are marked STOPPED and not resumed.
	orgReader := store.NewOrganizationReader(db)
	manager.SetOrgExists(orgReader.Exists)

	// Real send path: the Sender routes each request to the live per-session
	// whatsmeow client resolved from the manager (via outbound.WithSessionID on
	// the context). Sessions without a connected client return not_implemented.
	sender := outbound.NewSender(service.NewRoutingWAClient(manager), outboxAdapter, limiter, outbound.SystemClock())

	// Register this gateway's self-row in the gateways registry (§4.5/§7) before
	// the manager adopts sessions. Best-effort: a write failure is logged, not
	// fatal — the HTTP surface should still come up.
	if err := upsertGatewaySelfRow(ctx, st.Gateways, cfg); err != nil {
		log.Error("upsert gateway self-row failed", "gateway", cfg.GatewayID, "err", err)
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

	// Live-connection registry: lets the control bus drop NDJSON streams by
	// key/user/org on revocation (§4.6).
	streamRegistry := stream.NewConnRegistry()
	streamHandler := stream.NewHandler(stream.HandlerConfig{
		Redis:        rdb,
		Organization: stream.OrganizationAccessorFunc(organizationFromContext),
		LogReader:    service.NewEventLogReaderAdapter(st.EventLog),
		Principals:   stream.PrincipalAccessorFunc(streamIdentityFromContext),
		Registry:     streamRegistry,
		Log:          log,
	})

	// Control bus subscriber (§4.6): connects to PUBSUB_REDIS_URL (defaults to
	// REDIS_URL via config) and reacts to ctrl:* revocation messages by evicting
	// the api-key cache and dropping live streams. If both URLs are empty, skip
	// gracefully — the cache TTL remains the revocation backstop.
	controlStop := startControlBus(ctx, cfg.PubSubRedisURL, keyVerifier, streamRegistry, log)
	defer controlStop()

	h := handlers.New(services, streamHandler, log)
	router := gwhttp.NewRouter(gwhttp.RouterConfig{
		Handlers:    h,
		Tokens:      tokenVerifier,
		Keys:        keyVerifier,
		CORSOrigins: cfg.FrontendOrigins,
		Limiter:     nil, // HTTP-edge rate limiting optional; outbound limits sends.
		Readiness:   readiness(db, rdb),
		OpenAPIPath: "docs/openapi.yaml",
		Log:         log,
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("server stopped cleanly")
	return nil
}

// organizationFromContext is the stream.OrganizationAccessor: it reads the
// organization id the auth middleware lifted onto the request context.
func organizationFromContext(ctx context.Context) (string, bool) {
	id := httpx.OrganizationID(ctx)
	return id, id != ""
}

// streamIdentityFromContext is the stream.PrincipalAccessor: it lifts the
// verified caller's identity off the context so the control bus can drop a live
// stream by its api-key id / user id / org (§4.6).
func streamIdentityFromContext(ctx context.Context) (stream.ConnIdentity, bool) {
	p := authz.FromContext(ctx)
	if p == nil {
		return stream.ConnIdentity{}, false
	}
	return stream.ConnIdentity{
		KeyID:          p.KeyID,
		UserID:         p.UserID,
		OrganizationID: p.OrganizationID,
	}, true
}

// upsertGatewaySelfRow registers this gateway in the gateways registry (§4.5/§7):
// id=GATEWAY_ID, base_url=PUBLIC_URL, timestamps = epoch-ms now. created_at is
// preserved on update by the repo.
func upsertGatewaySelfRow(ctx context.Context, repo *store.GatewayRepo, cfg *config.Config) error {
	now := domain.NowMs()
	g := domain.Gateway{
		ID:         cfg.GatewayID,
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

// startControlBus connects to the control-bus Redis (§4.6) and starts the ctrl:*
// subscriber, returning a stop func for the shutdown sequence. When the URL is
// empty (no PUBSUB_REDIS_URL or REDIS_URL) it logs a warning and returns a no-op
// stop — the api-key cache TTL is the revocation backstop.
func startControlBus(ctx context.Context, pubsubURL string, cache controlbus.KeyCache, dropper controlbus.StreamDropper, log *slog.Logger) func() {
	if pubsubURL == "" {
		log.Warn("control bus disabled: PUBSUB_REDIS_URL/REDIS_URL empty; relying on api-key cache TTL for revocation")
		return func() {}
	}
	crdb, err := openRedis(pubsubURL)
	if err != nil {
		log.Error("control bus disabled: open pubsub redis failed", "err", err)
		return func() {}
	}
	sub := controlbus.New(crdb, cache, dropper, log)
	if err := sub.Start(ctx); err != nil {
		log.Error("control bus disabled: subscribe failed", "err", err)
		_ = crdb.Close()
		return func() {}
	}
	return func() {
		sub.Stop()
		_ = crdb.Close()
	}
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
	if !strings.Contains(dsn, "parseTime=") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "parseTime=true"
	}
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

func migrateUp(db *sql.DB) error {
	m, err := migrator(db)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// runMigrate implements `migrate up|down`.
func runMigrate(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	setupLogging(cfg.LogLevel)
	db, err := openMySQL(cfg.MySQLDSN)
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
