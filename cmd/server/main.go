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

	"github.com/ramaadi/quick-whatsapp-gateway/internal/auth"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/config"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/crypto"
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

	// --- Auth (Authula): build, seed RBAC, bootstrap super-admin ---
	authInst, err := auth.Build(auth.Config{
		AppName:          "quick-whatsapp-gateway",
		BaseURL:          cfg.PublicURL,
		Secret:           cfg.AppEncryptionKey,
		MySQLDSN:         cfg.MySQLDSN,
		RedisURL:         cfg.RedisURL,
		UserPanelEnabled: cfg.UserPanelEnabled,
		Logger:           log,
	})
	if err != nil {
		return fmt.Errorf("build auth: %w", err)
	}
	defer authInst.Close()

	if err := auth.SeedRBAC(ctx, authInst.RBAC()); err != nil {
		return fmt.Errorf("seed rbac: %w", err)
	}
	if cfg.AdminEmail != "" && cfg.AdminPassword != "" {
		if err := auth.BootstrapAdmin(ctx, authInst.Users(), authInst.SignUp(), authInst.RBAC(), cfg.AdminEmail, cfg.AdminPassword); err != nil {
			return fmt.Errorf("bootstrap admin: %w", err)
		}
	}

	// --- whatsmeow keystore ---
	keystore, err := wastore.Open(ctx, wastore.Driver(cfg.WhatsmeowStoreDriver), db, cfg.WhatsmeowStoreDSN, nil)
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
		DefaultRatePerMin:  cfg.DefaultRatePerMin,
		DefaultRatePerHour: cfg.DefaultRatePerHour,
		DefaultAutoRead:    cfg.DefaultAutoRead,
	})

	// Real send path: the Sender routes each request to the live per-session
	// whatsmeow client resolved from the manager (via outbound.WithSessionID on
	// the context). Sessions without a connected client return not_implemented.
	sender := outbound.NewSender(service.NewRoutingWAClient(manager), outboxAdapter, limiter, outbound.SystemClock())

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
	// Read-only better-auth api-key verifier (§4.2). The key hasher (which must
	// match the pinned better-auth scheme) is supplied by the authz package in a
	// later stage; nil here means Verify returns an internal error until wired.
	keyVerifier := service.NewKeyVerifier(st.APIKeys, nil, log)
	services := service.New(service.Deps{
		Store:                st,
		Manager:              manager,
		Sender:               sender,
		Crypto:               aes,
		Auth:                 authInst,
		DefaultRetryDelay:    cfg.WebhookRetryDelay,
		DefaultRetryAttempts: cfg.WebhookRetryAttempts,
		Log:                  log,
	})

	streamHandler := stream.NewHandler(stream.HandlerConfig{
		Redis:        rdb,
		Organization: stream.OrganizationAccessorFunc(organizationFromContext),
		LogReader:    service.NewEventLogReaderAdapter(st.EventLog),
		Log:          log,
	})

	h := handlers.New(services, streamHandler, log)
	router := gwhttp.NewRouter(gwhttp.RouterConfig{
		Handlers:    h,
		Auth:        authInst,
		Verifier:    keyVerifier,
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
