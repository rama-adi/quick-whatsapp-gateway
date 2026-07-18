package controlclient

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki/gatewayidentity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type Config struct {
	Target, GatewayID string
	Identity          *gatewayidentity.Manager
	AttemptTimeout    time.Duration
}
type Client struct {
	cfg  Config
	mu   sync.Mutex
	conn *grpc.ClientConn
}

func New(cfg Config) (*Client, error) {
	if cfg.Target == "" || cfg.GatewayID == "" || cfg.Identity == nil {
		return nil, errors.New("control client: invalid configuration")
	}
	if cfg.AttemptTimeout <= 0 {
		cfg.AttemptTimeout = 10 * time.Second
	}
	return &Client{cfg: cfg}, nil
}

func (c *Client) Ensure(ctx context.Context, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return nil
	}
	_ = c.cfg.Identity.Load()
	if !c.cfg.Identity.Ready() {
		if token == "" {
			return errors.New("control client: enrollment token required")
		}
		if err := c.enroll(ctx, token); err != nil {
			return err
		}
	}
	return c.connectAuthenticated(ctx)
}
func (c *Client) enroll(ctx context.Context, token string) error {
	pending, err := c.cfg.Identity.Prepare()
	if err != nil {
		return err
	}
	tlsConfig, err := pki.NewAPIPeerTLSConfig(c.cfg.Identity.BootstrapCA(), time.Now)
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(c.cfg.Target, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	client := gatewayv1.NewGatewayEnrollmentServiceClient(conn)
	for attempt := 0; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, c.cfg.AttemptTimeout)
		response, callErr := client.Enroll(attemptCtx, &gatewayv1.GatewayEnrollmentServiceEnrollRequest{Token: token, CsrDer: pending.CSRDER})
		cancel()
		if callErr == nil {
			return c.cfg.Identity.Install(gatewayidentity.Installation{GatewayID: response.GatewayId, ChainPEM: response.CertificateChainPem, TrustBundlePEM: response.TrustBundlePem, AuthorityID: response.AuthorityId, Serial: response.SerialNumber, NotBefore: response.NotBeforeUnixMs, NotAfter: response.NotAfterUnixMs})
		}
		if !retryable(status.Code(callErr)) {
			return errors.New("control client: enrollment rejected")
		}
		delay := time.Duration(100*(1<<min(attempt, 5)))*time.Millisecond + time.Duration(rand.IntN(100))*time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func retryable(code codes.Code) bool {
	switch code {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Aborted, codes.ResourceExhausted:
		return true
	default:
		return false
	}
}
func (c *Client) connectAuthenticated(ctx context.Context) error {
	tlsConfig, err := pki.NewAPIPeerTLSConfig(c.cfg.Identity.BootstrapCA(), time.Now)
	if err != nil {
		return err
	}
	tlsConfig.GetClientCertificate = c.cfg.Identity.GetClientCertificate
	conn, err := grpc.NewClient(c.cfg.Target, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, c.cfg.AttemptTimeout)
	defer cancel()
	response, err := gatewayv1.NewGatewayHealthServiceClient(conn).Check(checkCtx, &gatewayv1.GatewayHealthServiceCheckRequest{})
	if err != nil || response.Status != gatewayv1.ServingStatus_SERVING_STATUS_SERVING {
		_ = conn.Close()
		return errors.New("control client: private health unavailable")
	}
	c.conn = conn
	return nil
}
func (c *Client) Conn() *grpc.ClientConn { c.mu.Lock(); defer c.mu.Unlock(); return c.conn }
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}
