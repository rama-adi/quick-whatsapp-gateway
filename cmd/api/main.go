// Command api is the API/control-plane entrypoint and composition root: the single
// front door and trust boundary in front of the WhatsApp gateways. It loads the
// API configuration, opens the shared MySQL tables and Redis, builds the
// two-acceptor authenticator (better-auth JWKS + api-key table), serves every
// REST operation locally, and runs the public/private gRPC servers with graceful
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
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/redis/go-redis/v9"

	apigateway "github.com/rama-adi/quick-whatsapp-gateway/internal/api/gateway"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/apigrpc"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/config"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/controlbus"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/crypto"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/dbconn"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/dbmigrate"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/http/handlers"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/oidp"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki/apiidentity"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki/localmysql"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/router"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/service"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/service/gatewayadmin"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/stream"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/webhooks"
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
	// authn runs ONLY here; every operation serves API-locally. ---
	tokenVerifier, err := authz.NewJWTVerifier(cfg.BetterAuthJWKSURL, cfg.BetterAuthURL)
	if err != nil {
		return fmt.Errorf("build jwt verifier: %w", err)
	}
	baseKeyVerifier, err := authz.NewAPIKeyVerifier(st.APIKeys, authz.DefaultHasher())
	if err != nil {
		return fmt.Errorf("build api-key verifier: %w", err)
	}
	keyVerifier := authz.NewCachingKeyVerifier(baseKeyVerifier, authz.DefaultKeyCacheTTL)

	oidpSigner, err := oidp.NewSigner(st.OAuthSigningKeys, cfg.OIDCKeyEncKey)
	if err != nil {
		return fmt.Errorf("build oidc signer: %w", err)
	}
	aes, err := crypto.NewAESGCM(cfg.AppEncryptionKey)
	if err != nil {
		return fmt.Errorf("build app cipher: %w", err)
	}
	services := service.New(service.Deps{
		Store:                      st,
		Crypto:                     aes,
		OAuthClientSecretPepper:    cfg.OAuthClientSecretPepper,
		OIDCIssuer:                 cfg.OIDCIssuer,
		WhatsAppAdminCommandPrefix: cfg.WhatsAppAdminCmdPrefix,
		ControlPublisher:           service.NewRedisControlPublisher(rdb),
		DefaultRetryDelay:          cfg.WebhookRetryDelay,
		DefaultRetryAttempts:       cfg.WebhookRetryAttempts,
		Log:                        log,
	})
	apiHandlers := handlers.New(services, log)

	// --- Committed-event fan-out (Increment 5). Control-mode gateway events are
	// durable once the ingest transaction commits; this worker then claims each
	// committed envelope, fans it out to realtime and webhooks in that order,
	// and records completion only after every consumer accepts. A crash between
	// consumer acceptance and completion replays the event; consumers dedupe by
	// event id. The lease comfortably exceeds one dispatch attempt, so an expired
	// lease merely makes a crashed worker's claims retryable. ---
	const (
		committedEventLease        = 2 * time.Minute
		committedEventBatch        = 100
		committedEventPollInterval = 2 * time.Second
	)
	var publisher *stream.Publisher
	if rdb != nil {
		publisher = stream.NewPublisher(rdb, log)
	}
	webhookEnqueuer := webhooks.NewEnqueuer(
		service.NewWebhookRepoAdapter(st.Webhooks),
		service.NewWebhookDeliveryRepoAdapter(st.WebhookDeliveries),
		nil, log,
	)
	var oidpPending *oidp.PendingStore
	var oidpEvents *service.OIDPEventConsumer
	if rdb != nil {
		requestTTL := time.Duration(cfg.OIDCRequestTTLSeconds) * time.Second
		oidpPending = oidp.NewPendingStore(rdb, cfg.RedisPrefix, requestTTL)
		oidpEvents = service.NewOIDPEventConsumer(nil)
	}
	consumers := []application.CommittedEventConsumer{
		service.NewEventProjectionConsumer(service.NewStoreProjections(st), nil),
	}
	if oidpEvents != nil {
		// Run OAuth interception before the projection consumer so claim messages
		// are never published as ordinary chat content.
		consumers = append([]application.CommittedEventConsumer{oidpEvents}, consumers...)
	}
	committedConsumers := service.NewCommittedEventConsumers(consumers...)
	committedWorker, err := service.NewCommittedEventWorker(
		committedEventWorkStore{repo: st.GatewayEvents},
		service.NewCommittedEventDispatcher(
			// API-side WhatsApp-data projections (Increment 9): chats, messages,
			// polls, poll votes, receipt statuses, and identity captures are
			// derived from committed events, replacing the gateway's local
			// inbound-pipeline writes. They run before realtime/webhook fan-out.
			[]application.CommittedEventConsumer{committedConsumers},
			publisher, webhookEnqueuer),
		service.CommittedEventWorkerConfig{
			Owner: processOwner(),
			Lease: committedEventLease,
			Batch: committedEventBatch,
			Poll:  committedEventPollInterval,
			Now:   time.Now,
		})
	if err != nil {
		return fmt.Errorf("build committed event worker: %w", err)
	}
	workerCtx, workerStop := context.WithCancel(ctx)
	defer workerStop()

	// --- Poll recap scheduling is API-owned (Increment 5): the durable MySQL
	// sweep is the source of truth, and recap events append/publish/enqueue
	// beside this process's other fan-out instead of on a gateway. The Redis
	// sorted set is only a low-latency wake-up index. ---
	// --- Webhook dispatch (API-owned). Enqueue happens in the committed-event
	// fan-out above; this loop claims due deliveries and performs the HTTP
	// sends with HMAC + retries, replacing the legacy gateway ticker. ---
	dispatcher := webhooks.NewDispatcher(
		service.NewWebhookRepoAdapter(st.Webhooks),
		service.NewWebhookDeliveryRepoAdapter(st.WebhookDeliveries),
		st.EventLog,
		&http.Client{Timeout: 30 * time.Second},
		aes,
		nil,
		log,
	)
	dispatchStop := startWebhookDispatchLoop(ctx, dispatcher, log)
	defer dispatchStop()

	pollRecaps := service.NewPollRecapWorker(st, publisher, webhookEnqueuer, rdb, service.PollRecapConfig{
		RedisPrefix: cfg.RedisPrefix,
		Log:         log,
	})
	pollRecapStop := pollRecaps.Start(ctx)
	defer pollRecapStop()

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
			LogReader: service.NewEventLogReaderAdapter(st.EventLog),
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
			Pending:      oidpPending,
			WebLoginURL:  cfg.WebLoginURL,
			Issuer:       cfg.OIDCIssuer,
			SecretPepper: cfg.OAuthClientSecretPepper,
			RequestTTL:   requestTTL,
			AuthCodeTTL:  time.Duration(cfg.OIDCAuthCodeTTLSeconds) * time.Second,
			TrustProxy:   cfg.OIDCTrustProxy,
		})
	}

	var oidpControl *oidp.AppChangeSubscriber

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
	var engineClient *apigateway.EngineClient
	var oidpInterceptor *oidp.LoginInterceptor
	defer func() {
		if engineClient != nil {
			_ = engineClient.Close()
		}
	}()
	if cfg.GatewayGRPCAddr != "" {
		policy, policyErr := cfg.GatewayPKI.Policy()
		if policyErr != nil {
			return fmt.Errorf("build gateway PKI policy: %w", policyErr)
		}
		signer, signerErr := localmysql.New(db, localmysql.Config{
			KEK:             cfg.GatewayPKI.EncryptionKey,
			KeyID:           cfg.GatewayPKI.EncryptionKeyID,
			RootTTL:         cfg.GatewayPKI.RootTTL,
			IntermediateTTL: cfg.GatewayPKI.IntermediateTTL,
			RenewBefore:     cfg.GatewayPKI.IntermediateRenewBefore,
			Policy:          policy,
		})
		if signerErr != nil {
			return fmt.Errorf("build gateway PKI signer: %w", signerErr)
		}
		if signerErr = signer.EnsureHierarchy(ctx); signerErr != nil {
			return fmt.Errorf("ensure gateway PKI hierarchy: %w", signerErr)
		}
		identity, identityErr := apiidentity.New(
			apiidentity.Config{
				Directory:   cfg.GatewayTLSIdentityDir,
				RenewBefore: cfg.GatewayTLSRenewBefore,
			},
			signer,
		)
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
			return errors.New("load gateway trust bundle: invalid PEM")
		}
		engineClient, identityErr = apigateway.NewEngineClient(
			st.Gateways,
			apigateway.NewEngineMTLSDial(identity.GetCertificate, clientRoots),
			cfg.GatewayEngineUnaryDeadline,
			cfg.GatewayEngineSendDeadline,
		)
		if identityErr != nil {
			return fmt.Errorf("build gateway engine client: %w", identityErr)
		}
		services.Sessions.SetGatewayLiveFacade(engineClient)
		services.Presence.SetGatewayLiveFacade(engineClient)

		// --- API-local live resources (Increment 7): contacts, groups, chat
		// presence, and admin backfill execute through private engine RPCs. The
		// engine returns raw live results; these services persist their own
		// shared-MySQL projections exactly as the legacy in-process path did.
		liveFacade := apigateway.NewLiveOpsFacade(engineClient)
		services.Contacts.SetGatewayContactFacade(liveFacade)
		services.Groups.SetGatewayGroupFacade(liveFacade)
		services.Chats.SetGatewayChatFacade(liveFacade)
		services.Admin.SetGatewayBackfillFacade(liveFacade)

		// --- API-owned session lifecycle (Increment 7): the API picks the
		// placement, owns the row + assignment, and drives the five live engine
		// calls (prepare/QR/pairing-code/logout/forget) through the private
		// engine. The OAuth cascade stays with the service; the facade covers
		// only the live parts. ---
		assignments := store.NewGatewayAssignmentRepo(db)
		services.Sessions.SetGatewayAssignmentRepo(assignments)
		services.Sessions.SetGatewaySessionFacade(apigateway.NewSessionLifecycleFacade(engineClient))

		// --- API-owned outbound scheduling (Increment 6): durable command rows,
		// product rate limits, and retry/backoff decisions run here; the gateway
		// executes each command at most once per command id. The lease must
		// exceed the engine send deadline so an in-flight dispatch is never
		// reclaimed mid-flight. ---
		outboundScheduler, schedulerErr := service.NewOutboundScheduler(
			st.Sessions,
			st.Outbox,
			engineClient,
			service.NewRedisRateLimiter(rdb),
			service.OutboundSchedulerConfig{
				Lease:       cfg.GatewayEngineSendDeadline + time.Minute,
				Batch:       32,
				Poll:        2 * time.Second,
				MaxAttempts: 10,
				BackoffBase: 5 * time.Second,
				BackoffCap:  10 * time.Minute,
				Now:         time.Now,
			},
			log,
		)
		if schedulerErr != nil {
			return fmt.Errorf("build outbound scheduler: %w", schedulerErr)
		}
		schedulerCtx, schedulerStop := context.WithCancel(ctx)
		defer schedulerStop()
		go func() {
			if err := outboundScheduler.Run(schedulerCtx); err != nil && !errors.Is(err, context.Canceled) {
				log.Warn("outbound scheduler stopped", "err", err)
			}
		}()
		if services.Messages == nil {
			return errors.New("message service is not constructed")
		}
		services.Messages.SetGatewaySendFacade(outboundScheduler)
		services.Messages.SetGatewayOpFacade(outboundScheduler)
		if oidpPending != nil {
			oidpInterceptor = oidp.NewLoginInterceptor(
				st.OAuthClients,
				oidpPending,
				service.NewOIDPGroupMemberChecker(st.GroupMembers),
				service.NewOIDPBotFeedback(outboundScheduler),
				log,
			)
			oidpEvents.SetInterceptor(oidpInterceptor)
		}
		services.Sessions.SetSessionDesiredController(sessionDesiredController{
			assignments: store.NewGatewayAssignmentRepo(db),
		})
		tlsConfig := privateGatewayTLSConfig(identity, clientRoots)
		enrollment, enrollmentErr := service.NewEnrollmentService(db, signer, service.DefaultEnrollmentConfig())
		if enrollmentErr != nil {
			return fmt.Errorf("build enrollment service: %w", enrollmentErr)
		}
		gatewayAdminService, enrollmentErr := gatewayadmin.NewWithActions(
			st.Gateways,
			st.Sessions,
			st.AuditEvents,
			enrollment,
			store.NewGatewayAdminStore(db),
		)
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
			Store: gatewayControlStore{
				repo:           st.Gateways,
				reconciliation: store.NewGatewayReconciliationRepo(db),
				eventIngest:    store.NewGatewayEventIngestRepo(db),
			},
			ResolveGatewayID: func(ctx context.Context) (string, bool) {
				identity, ok := gatewayIdentityFromContext(ctx)
				return identity.GatewayID, ok
			},
		}
		privateGRPCServer = newPrivateGatewayGRPCServer(
			tlsConfig,
			privateGatewayAuthenticator{store: mysqlGatewayCredentialStore{db: db}},
			enrollment,
			renewal,
			privateReady,
			control,
		)
		go renewAPIIdentity(ctx, identity, cfg.GatewayTLSRenewBefore, log)
	}
	if rdb != nil && oidpProvider != nil {
		var oidpInvalidator oidp.AppInvalidator
		if oidpInterceptor != nil {
			oidpInvalidator = oidpInterceptor
		}
		oidpControl = oidp.NewControlSubscriber(
			rdb,
			oidpInvalidator,
			oidpProvider,
			oidpProvider.PendingStore(),
			log,
		)
		if err := oidpControl.Start(ctx); err != nil {
			log.Warn("oidp control bus subscriber disabled", "err", err)
		} else {
			defer oidpControl.Stop()
		}
	}
	go func() {
		if err := committedWorker.Run(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("committed event worker stopped", "err", err)
		}
	}()
	srv, err := router.NewServer(router.Config{
		Tokens:           tokenVerifier,
		Keys:             keyVerifier,
		Sessions:         st.Sessions,
		CORSOrigins:      cfg.FrontendOrigins,
		Readiness:        readinessGate.check,
		DBStats:          db.Stats,
		OpenAPIPath:      "docs/openapi.yaml",
		MessageHandlers:  apiHandlers,
		ResourceHandlers: apiHandlers,
		Redis:            rdb,
		Pump:             pump,
		Registry:         registry,
		RedisPrefix:      cfg.RedisPrefix,
		PublicURL:        cfg.PublicURL,
		OIDCIssuer:       cfg.OIDCIssuer,
		OIDPSigner:       oidpSigner,
		OIDPProvider:     oidpProvider,
		OAuthHandlers:    apiHandlers,
		AdminHandlers:    apiHandlers,
		Log:              log,
	})
	if err != nil {
		return fmt.Errorf("build router: %w", err)
	}

	grpcServer := newPublicGRPCServer(readinessGate.check, apigrpc.Deps{
		Tokens:   tokenVerifier,
		Keys:     keyVerifier,
		Sessions: services.Sessions,
		Messages: services.Messages,
		Chats:    services.Chats,
		Events:   st.EventLog,
	})
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
			log.Info(
				"api listening",
				"http_addr", cfg.HTTPAddr,
				"public_grpc_addr", cfg.PublicGRPCAddr,
				"private_gateway_grpc_enabled", cfg.GatewayGRPCAddr != "",
			)
		},
	}
	if err := runner.run(ctx); err != nil {
		return err
	}
	log.Info("api stopped cleanly")
	return nil
}

func prepareAPIDatabase(
	dsn string,
	migrate func(string, dbmigrate.Direction) error,
	open func(string) (*sql.DB, error),
) (*sql.DB, error) {
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
		return errors.New("usage: api oidp rotate-key generate-next|promote <kid>|retire <kid>")
	}
	if cfg.MySQLDSN == "" {
		return errors.New("config: MYSQL_DSN is required")
	}
	if cfg.OIDCKeyEncKey == "" {
		return errors.New("config: OIDC_KEY_ENC_KEY is required")
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
			return errors.New("usage: api oidp rotate-key promote <kid>")
		}
		return oidp.PromoteNextKey(ctx, repo, args[1], now)
	case "retire":
		if len(args) != 2 {
			return errors.New("usage: api oidp rotate-key retire <kid>")
		}
		return oidp.RetireKey(ctx, repo, args[1], now)
	default:
		return errors.New("usage: api oidp rotate-key generate-next|promote <kid>|retire <kid>")
	}
}

// startControlBus subscribes to the ctrl:* revocation bus and evicts the api-key
// cache + drops matching live WebSocket connections. Empty URL → no-op (the cache
// TTL is the revocation backstop).
func startControlBus(
	ctx context.Context,
	pubsubURL string,
	cache controlbus.KeyCache,
	dropper controlbus.StreamDropper,
	log *slog.Logger,
) func() {
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

// startWebhookDispatchLoop runs the webhook dispatcher on a ticker until ctx
// is done. It is the API-side replacement for the legacy gateway dispatch loop.
func startWebhookDispatchLoop(ctx context.Context, d *webhooks.Dispatcher, log *slog.Logger) func() {
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

// committedEventWorkStore adapts the gateway-event ingest repo to the service
// worker's durable work port. It stays in the composition root so
// internal/store keeps no application-layer imports.
type committedEventWorkStore struct{ repo *store.GatewayEventIngestRepo }

func (a committedEventWorkStore) ClaimCommittedEvents(
	ctx context.Context,
	claim application.CommittedEventClaim,
) ([]domain.Event, error) {
	return a.repo.ClaimCommittedEvents(ctx, store.CommittedEventWork{
		Owner: claim.Owner, ClaimedAt: claim.ClaimedAt, LeaseUntil: claim.LeaseUntil, MaxItems: claim.MaxItems,
	})
}

func (a committedEventWorkStore) CompleteCommittedEvent(ctx context.Context, owner, eventID string) error {
	return a.repo.CompleteCommittedEvent(ctx, owner, eventID, time.Now())
}

// processOwner identifies this replica in committed-event claims. A hostname is
// stable across restarts on one machine and disjoint across replicas.
func processOwner() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "api"
	}
	return host
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
