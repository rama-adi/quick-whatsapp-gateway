// Command api is the API/control-plane entrypoint and composition root: the single
// front door and trust boundary in front of the WhatsApp gateways. It loads the
// API configuration, opens the shared MySQL routing table and Redis control
// bus, builds the two-acceptor authenticator (better-auth JWKS + api-key table)
// and the Ed25519 assertion minter, and runs the HTTP broker with graceful
// shutdown. See docs/specs/router.md.
package main

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/redis/go-redis/v9"

	apigateway "github.com/ramaadi/quick-whatsapp-gateway/internal/api/gateway"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/assertion"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/config"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/controlbus"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/dbconn"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/dbmigrate"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/http/handlers"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/oidp"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki/apiidentity"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki/localmysql"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/router"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service/gatewayadmin"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/stream"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadAPI()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	setupLogging(cfg.LogLevel)
	log := slog.Default()

	if len(os.Args) >= 3 && os.Args[1] == "oidp" && os.Args[2] == "rotate-key" {
		return runOIDPRotateKey(context.Background(), cfg, os.Args[3:])
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- API-owned WA schema, then shared routing table (MySQL) ---
	db, err := prepareAPIDatabase(cfg.MySQLDSN, dbmigrate.Run, dbconn.OpenMySQL)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	prometheus.MustRegister(collectors.NewDBStatsCollector(db, "router"))
	st := store.New(db)

	// --- Redis (control bus; realtime in Increment B) ---
	var rdb *redis.Client
	if cfg.RedisURL != "" {
		rdb, err = dbconn.OpenRedis(cfg.RedisURL)
		if err != nil {
			return fmt.Errorf("open redis: %w", err)
		}
		defer func() { _ = rdb.Close() }()
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
	oidpSigner, err := oidp.NewSigner(st.OAuthSigningKeys, cfg.OIDCKeyEncKey)
	if err != nil {
		return fmt.Errorf("build oidc signer: %w", err)
	}
	services := service.New(service.Deps{
		Store:                      st,
		OAuthClientSecretPepper:    cfg.OAuthClientSecretPepper,
		OIDCIssuer:                 cfg.OIDCIssuer,
		WhatsAppAdminCommandPrefix: cfg.WhatsAppAdminCmdPrefix,
		ControlPublisher:           service.NewRedisControlPublisher(rdb),
		Log:                        log,
	})
	apiHandlers := handlers.New(services, log)

	// --- Realtime (Increment B): the router is the single client-facing realtime
	// endpoint. It subscribes to the shared Redis evt:* fan-out (the gateways keep
	// publishing there) and serves a WebSocket per single-use ticket. The live
	// registry lets the control bus drop connections on revocation. ---
	var (
		pump     *stream.Pump
		registry *stream.ConnRegistry
	)
	if rdb != nil {
		registry = stream.NewConnRegistry()
		pump = stream.NewPump(stream.PumpConfig{
			Redis:     rdb,
			LogReader: &eventLogReader{repo: st.EventLog},
			Log:       log,
		})
	}
	var oidpProvider *oidp.Provider
	if rdb != nil {
		requestTTL := time.Duration(cfg.OIDCRequestTTLSeconds) * time.Second
		oidpProvider = oidp.NewProvider(oidp.ProviderConfig{
			Clients:      st.OAuthClients,
			Sessions:     st.Sessions,
			Groups:       st.Groups,
			Identities:   st.Identities,
			Grants:       st.OAuthGrants,
			Refresh:      st.OAuthRefresh,
			Signer:       oidpSigner,
			Pending:      oidp.NewPendingStore(rdb, cfg.RedisPrefix, requestTTL),
			WebLoginURL:  cfg.WebLoginURL,
			Issuer:       cfg.OIDCIssuer,
			SecretPepper: cfg.OAuthClientSecretPepper,
			RequestTTL:   requestTTL,
			AuthCodeTTL:  time.Duration(cfg.OIDCAuthCodeTTLSeconds) * time.Second,
			TrustProxy:   cfg.OIDCTrustProxy,
		})
	}

	if rdb != nil && oidpProvider != nil {
		oidpControl := oidp.NewControlSubscriber(rdb, oidpProvider, oidpProvider, oidpProvider.PendingStore(), log)
		if err := oidpControl.Start(ctx); err != nil {
			log.Warn("oidp control bus subscriber disabled", "err", err)
		} else {
			defer oidpControl.Stop()
		}
	}

	// --- Control bus subscriber (§4.6): the router owns the api-key cache and the
	// live-connection registry, so it subscribes to ctrl:* and evicts the cache +
	// drops live WebSocket connections on revocation (D7). ---
	var dropper controlbus.StreamDropper
	if registry != nil {
		dropper = registry
	}
	controlStop := startControlBus(ctx, cfg.PubSubRedisURL, keyVerifier, dropper, log)
	defer controlStop()

	readinessGate := &readinessGate{dependencies: readiness(db, rdb)}
	var privateGRPCServer grpcLifecycle
	var privateReady func() error
	if cfg.GatewayGRPCAddr != "" {
		policy, policyErr := cfg.GatewayPKI.Policy()
		if policyErr != nil {
			return fmt.Errorf("build gateway PKI policy: %w", policyErr)
		}
		signer, signerErr := localmysql.New(db, localmysql.Config{KEK: cfg.GatewayPKI.EncryptionKey, KeyID: cfg.GatewayPKI.EncryptionKeyID, RootTTL: cfg.GatewayPKI.RootTTL, IntermediateTTL: cfg.GatewayPKI.IntermediateTTL, RenewBefore: cfg.GatewayPKI.IntermediateRenewBefore, Policy: policy})
		if signerErr != nil {
			return fmt.Errorf("build gateway PKI signer: %w", signerErr)
		}
		if signerErr = signer.EnsureHierarchy(ctx); signerErr != nil {
			return fmt.Errorf("ensure gateway PKI hierarchy: %w", signerErr)
		}
		identity, identityErr := apiidentity.New(apiidentity.Config{Directory: cfg.GatewayTLSIdentityDir, RenewBefore: cfg.GatewayTLSRenewBefore}, signer)
		if identityErr != nil {
			return fmt.Errorf("build API TLS identity: %w", identityErr)
		}
		if identityErr = identity.Ensure(ctx); identityErr != nil {
			return fmt.Errorf("ensure API TLS identity: %w", identityErr)
		}
		bundle, bundleErr := signer.TrustBundle()
		if bundleErr != nil {
			return fmt.Errorf("load gateway trust bundle: %w", bundleErr)
		}
		clientRoots := x509.NewCertPool()
		if !clientRoots.AppendCertsFromPEM(bundle) {
			return fmt.Errorf("load gateway trust bundle: invalid PEM")
		}
		tlsConfig := privateGatewayTLSConfig(identity, clientRoots)
		enrollment, enrollmentErr := service.NewEnrollmentService(db, signer, service.DefaultEnrollmentConfig())
		if enrollmentErr != nil {
			return fmt.Errorf("build enrollment service: %w", enrollmentErr)
		}
		gatewayAdminService, enrollmentErr := gatewayadmin.NewWithActions(st.Gateways, st.Sessions, st.AuditEvents, enrollment, store.NewGatewayAdminStore(db))
		if enrollmentErr != nil {
			return fmt.Errorf("build gateway administration service: %w", enrollmentErr)
		}
		apiHandlers.GatewayAdmin = gatewayAdminService
		renewal, renewalErr := service.NewRenewalService(db, signer)
		if renewalErr != nil {
			return fmt.Errorf("build renewal service: %w", renewalErr)
		}
		privateReady = func() error {
			if !identity.Ready() {
				return errors.New("API TLS identity unavailable")
			}
			if _, err := signer.TrustBundle(); err != nil {
				return fmt.Errorf("gateway PKI signer unavailable: %w", err)
			}
			return readiness(db, nil)()
		}
		control := &apigateway.Server{
			Store: gatewayControlStore{repo: st.Gateways},
			ResolveGatewayID: func(ctx context.Context) (string, bool) {
				identity, ok := gatewayIdentityFromContext(ctx)
				return identity.GatewayID, ok
			},
		}
		privateGRPCServer = newPrivateGatewayGRPCServer(tlsConfig, privateGatewayAuthenticator{store: mysqlGatewayCredentialStore{db: db}}, enrollment, renewal, privateReady, control)
		go renewAPIIdentity(ctx, identity, cfg.GatewayTLSRenewBefore, log)
	}
	srv, err := router.NewServer(router.Config{
		Sessions:      st.Sessions,
		Gateways:      st.Gateways,
		Minter:        minter,
		Tokens:        tokenVerifier,
		Keys:          keyVerifier,
		CORSOrigins:   cfg.FrontendOrigins,
		Readiness:     readinessGate.check,
		DBStats:       db.Stats,
		OpenAPIPath:   "docs/openapi.yaml",
		Redis:         rdb,
		Pump:          pump,
		Registry:      registry,
		RedisPrefix:   cfg.RedisPrefix,
		PublicURL:     cfg.PublicURL,
		OIDCIssuer:    cfg.OIDCIssuer,
		OIDPSigner:    oidpSigner,
		OIDPProvider:  oidpProvider,
		OAuthHandlers: apiHandlers,
		AdminHandlers: apiHandlers,
		Log:           log,
	})
	if err != nil {
		return fmt.Errorf("build router: %w", err)
	}

	grpcServer := newPublicGRPCServer(readinessGate.check)
	runner := &apiServerRunner{
		httpAddr:          cfg.HTTPAddr,
		grpcAddr:          cfg.PublicGRPCAddr,
		httpHandler:       srv.Handler(),
		grpcServer:        grpcServer,
		privateGRPCAddr:   cfg.GatewayGRPCAddr,
		privateGRPCServer: privateGRPCServer,
		readiness:         readinessGate,
		shutdownTimeout:   15 * time.Second,
		onBound: func(_, _, _ net.Listener) {
			log.Info("api listening", "http_addr", cfg.HTTPAddr, "public_grpc_addr", cfg.PublicGRPCAddr, "private_gateway_grpc_enabled", cfg.GatewayGRPCAddr != "", "issuer", cfg.Issuer, "kid", minter.KeyID())
		},
	}
	if err := runner.run(ctx); err != nil {
		return err
	}
	log.Info("api stopped cleanly")
	return nil
}

func prepareAPIDatabase(dsn string, migrate func(string, dbmigrate.Direction) error, open func(string) (*sql.DB, error)) (*sql.DB, error) {
	if err := migrate(dsn, dbmigrate.Up); err != nil {
		return nil, fmt.Errorf("run API schema migrations: %w", err)
	}
	db, err := open(dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	return db, nil
}

func runOIDPRotateKey(ctx context.Context, cfg *config.APIConfig, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: api oidp rotate-key generate-next|promote <kid>|retire <kid>")
	}
	if cfg.MySQLDSN == "" {
		return fmt.Errorf("config: MYSQL_DSN is required")
	}
	if cfg.OIDCKeyEncKey == "" {
		return fmt.Errorf("config: OIDC_KEY_ENC_KEY is required")
	}
	db, err := dbconn.OpenMySQL(cfg.MySQLDSN)
	if err != nil {
		return fmt.Errorf("open mysql: %w", err)
	}
	defer func() { _ = db.Close() }()
	repo := store.New(db).OAuthSigningKeys
	now := time.Now().UnixMilli()
	switch args[0] {
	case "generate-next":
		kid, err := oidp.GenerateNextKey(ctx, repo, cfg.OIDCKeyEncKey, now)
		if err != nil {
			return err
		}
		fmt.Println(kid)
		return nil
	case "promote":
		if len(args) != 2 {
			return fmt.Errorf("usage: api oidp rotate-key promote <kid>")
		}
		return oidp.PromoteNextKey(ctx, repo, args[1], now)
	case "retire":
		if len(args) != 2 {
			return fmt.Errorf("usage: api oidp rotate-key retire <kid>")
		}
		return oidp.RetireKey(ctx, repo, args[1], now)
	default:
		return fmt.Errorf("usage: api oidp rotate-key generate-next|promote <kid>|retire <kid>")
	}
}

// startControlBus subscribes to the ctrl:* revocation bus and evicts the api-key
// cache + drops matching live WebSocket connections. Empty URL → no-op (the cache
// TTL is the revocation backstop).
func startControlBus(ctx context.Context, pubsubURL string, cache controlbus.KeyCache, dropper controlbus.StreamDropper, log *slog.Logger) func() {
	if pubsubURL == "" {
		log.Warn("control bus disabled: PUBSUB_REDIS_URL/REDIS_URL empty; relying on api-key cache TTL for revocation")
		return func() {}
	}
	crdb, err := dbconn.OpenRedis(pubsubURL)
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

// eventLogReader adapts *store.EventLogRepo to stream.EventLogReader for the
// realtime pump's ?since= replay: it resolves the opaque event-id cursor to the
// store's monotonic id, then pages. Kept here (rather than importing the service
// graph) so the API binary stays lean.
type eventLogReader struct{ repo *store.EventLogRepo }

func (a *eventLogReader) ListSince(ctx context.Context, organization, session, afterEventID string, limit int) ([]domain.EventLogEntry, error) {
	var afterID uint64
	if afterEventID != "" {
		if entry, err := a.repo.GetByEventID(ctx, afterEventID); err == nil {
			afterID = entry.ID
		}
	}
	return a.repo.ListSince(ctx, organization, session, afterID, limit)
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
