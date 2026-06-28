// Command router is the central router entrypoint and composition root: the single
// front door and trust boundary in front of the WhatsApp gateways. It loads the
// router configuration, opens the shared MySQL routing table and Redis control
// bus, builds the two-acceptor authenticator (better-auth JWKS + api-key table)
// and the Ed25519 assertion minter, and runs the HTTP broker with graceful
// shutdown. See docs/specs/router.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/assertion"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/config"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/controlbus"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/dbconn"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/router"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("router exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadRouter()
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

	// --- Shared routing table (MySQL): wa_sessions + gateways registry ---
	db, err := dbconn.OpenMySQL(cfg.MySQLDSN)
	if err != nil {
		return fmt.Errorf("open mysql: %w", err)
	}
	defer db.Close()
	st := store.New(db)

	// --- Redis (control bus; realtime in Increment B) ---
	var rdb *redis.Client
	if cfg.RedisURL != "" {
		rdb, err = dbconn.OpenRedis(cfg.RedisURL)
		if err != nil {
			return fmt.Errorf("open redis: %w", err)
		}
		defer rdb.Close()
	}

	// --- Trust boundary: authenticate end-user callers (D2). The two-acceptor
	// authn that used to run on every gateway now runs ONLY here. ---
	tokenVerifier, err := authz.NewJWTVerifier(cfg.BetterAuthJWKSURL, cfg.BetterAuthURL)
	if err != nil {
		return fmt.Errorf("build jwt verifier: %w", err)
	}
	baseKeyVerifier, err := authz.NewAPIKeyVerifier(st.APIKeys, authz.DefaultHasher())
	if err != nil {
		return fmt.Errorf("build api-key verifier: %w", err)
	}
	keyVerifier := authz.NewCachingKeyVerifier(baseKeyVerifier, authz.DefaultKeyCacheTTL)

	// --- Internal assertion minter (router→gateway trust, D3) ---
	priv, _, err := assertion.ParsePrivateKey(cfg.Ed25519PrivateKey)
	if err != nil {
		return fmt.Errorf("parse router signing key: %w", err)
	}
	minter, err := assertion.NewMinter(priv, cfg.Issuer)
	if err != nil {
		return fmt.Errorf("build assertion minter: %w", err)
	}

	// --- Control bus subscriber (§4.6): the router now owns the api-key cache, so
	// it subscribes to ctrl:* and evicts on revocation. (Stream drop arrives with
	// the realtime endpoint in Increment B.) ---
	controlStop := startControlBus(ctx, cfg.PubSubRedisURL, keyVerifier, log)
	defer controlStop()

	srv, err := router.NewServer(router.Config{
		Sessions:    st.Sessions,
		Gateways:    st.Gateways,
		Minter:      minter,
		Tokens:      tokenVerifier,
		Keys:        keyVerifier,
		CORSOrigins: cfg.FrontendOrigins,
		Readiness:   readiness(db, rdb),
		OpenAPIPath: "docs/openapi.yaml",
		Log:         log,
	})
	if err != nil {
		return fmt.Errorf("build router: %w", err)
	}

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("router listening", "addr", cfg.HTTPAddr, "issuer", cfg.Issuer, "kid", minter.KeyID())
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("router stopped cleanly")
	return nil
}

// startControlBus subscribes to the ctrl:* revocation bus and evicts the api-key
// cache. A nil dropper is passed for now; the live-WS registry joins in Increment
// B. Empty URL → no-op (the cache TTL is the revocation backstop).
func startControlBus(ctx context.Context, pubsubURL string, cache controlbus.KeyCache, log *slog.Logger) func() {
	if pubsubURL == "" {
		log.Warn("control bus disabled: PUBSUB_REDIS_URL/REDIS_URL empty; relying on api-key cache TTL for revocation")
		return func() {}
	}
	crdb, err := dbconn.OpenRedis(pubsubURL)
	if err != nil {
		log.Error("control bus disabled: open pubsub redis failed", "err", err)
		return func() {}
	}
	sub := controlbus.New(crdb, cache, nil, log)
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

func readiness(db interface{ PingContext(context.Context) error }, rdb *redis.Client) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			return fmt.Errorf("mysql: %w", err)
		}
		if rdb != nil {
			if err := rdb.Ping(ctx).Err(); err != nil {
				return fmt.Errorf("redis: %w", err)
			}
		}
		return nil
	}
}

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
