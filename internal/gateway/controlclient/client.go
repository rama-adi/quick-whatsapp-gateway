package controlclient

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki/gatewayidentity"
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

// RenewalTransport isolates the generated private renewal RPC from credential
// rollover. Implementations must use the supplied incumbent connection to
// authenticate the renewal request.
type RenewalTransport interface {
	RenewCertificate(context.Context, *grpc.ClientConn, []byte) (gatewayidentity.Installation, error)
}

// RenewalFunc adapts a function into RenewalTransport.
type RenewalFunc func(
	context.Context,
	*grpc.ClientConn,
	[]byte,
) (gatewayidentity.Installation, error)

func (f RenewalFunc) RenewCertificate(
	ctx context.Context,
	conn *grpc.ClientConn,
	csrDER []byte,
) (gatewayidentity.Installation, error) {
	return f(ctx, conn, csrDER)
}

// GRPCRenewalTransport invokes the generated private enrollment client over
// the incumbent mTLS connection.
type GRPCRenewalTransport struct{}

func (GRPCRenewalTransport) RenewCertificate(
	ctx context.Context,
	conn *grpc.ClientConn,
	csrDER []byte,
) (gatewayidentity.Installation, error) {
	response, err := gatewayv1.NewGatewayEnrollmentServiceClient(conn).Renew(
		ctx,
		&gatewayv1.GatewayEnrollmentServiceRenewRequest{CsrDer: csrDER},
	)
	if err != nil {
		return gatewayidentity.Installation{}, err
	}
	return installationFromRenewal(response), nil
}

func installationFromRenewal(response *gatewayv1.GatewayEnrollmentServiceRenewResponse) gatewayidentity.Installation {
	return gatewayidentity.Installation{
		GatewayID:      response.GatewayId,
		ChainPEM:       response.CertificateChainPem,
		TrustBundlePEM: response.TrustBundlePem,
		AuthorityID:    response.AuthorityId,
		Serial:         response.SerialNumber,
		NotBefore:      response.NotBeforeUnixMs,
		NotAfter:       response.NotAfterUnixMs,
	}
}

type Client struct {
	cfg  Config
	mu   sync.Mutex
	conn *grpc.ClientConn
}

// Renewal is a staged credential rollover. The replacement connection is
// current so reconnecting users pick it up, but the incumbent remains open
// until the caller proves the replacement control stream is authenticated.
type Renewal struct {
	client                 *Client
	incumbent, replacement *grpc.ClientConn
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

// Renew obtains a replacement identity over the incumbent authenticated
// connection, validates and publishes it through the identity manager, and
// creates a new reusable mTLS connection before closing the old one. A failed
// renewal leaves the incumbent connection in place.
func (c *Client) Renew(ctx context.Context, transport RenewalTransport) error {
	renewal, err := c.BeginRenewal(ctx, transport)
	if err != nil {
		return err
	}
	return renewal.Commit()
}

// BeginRenewal stages a replacement connection without retiring the
// incumbent. Call Commit only after an authenticated use of the replacement
// has succeeded; Rollback restores the incumbent for every failed proof.
func (c *Client) BeginRenewal(ctx context.Context, transport RenewalTransport) (*Renewal, error) {
	if transport == nil {
		return nil, errors.New("control client: renewal transport required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || !c.cfg.Identity.Ready() {
		return nil, errors.New("control client: authenticated connection required")
	}
	pending, err := c.cfg.Identity.Prepare()
	if err != nil {
		return nil, err
	}
	installation, err := transport.RenewCertificate(ctx, c.conn, pending.CSRDER)
	if err != nil {
		return nil, err
	}
	if err = c.cfg.Identity.Install(installation); err != nil {
		return nil, err
	}
	replacement, err := c.newAuthenticatedConn()
	if err != nil {
		return nil, err
	}
	incumbent := c.conn
	c.conn = replacement
	return &Renewal{client: c, incumbent: incumbent, replacement: replacement}, nil
}

// Commit retires the incumbent after the replacement has been independently
// authenticated by the caller.
func (r *Renewal) Commit() error {
	if r == nil || r.client == nil || r.incumbent == nil || r.replacement == nil {
		return errors.New("control client: invalid renewal")
	}
	r.client.mu.Lock()
	defer r.client.mu.Unlock()
	if r.client.conn != r.replacement {
		return errors.New("control client: replacement is no longer current")
	}
	err := r.incumbent.Close()
	r.incumbent = nil
	return err
}

// Rollback restores the incumbent and closes the unproven replacement.
func (r *Renewal) Rollback() error {
	if r == nil || r.client == nil || r.incumbent == nil || r.replacement == nil {
		return errors.New("control client: invalid renewal")
	}
	r.client.mu.Lock()
	defer r.client.mu.Unlock()
	if r.client.conn != r.replacement {
		return errors.New("control client: replacement is no longer current")
	}
	r.client.conn = r.incumbent
	err := r.replacement.Close()
	r.replacement = nil
	return err
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
		response, callErr := client.Enroll(
			attemptCtx,
			&gatewayv1.GatewayEnrollmentServiceEnrollRequest{Token: token, CsrDer: pending.CSRDER},
		)
		cancel()
		if callErr == nil {
			return c.cfg.Identity.Install(gatewayidentity.Installation{
				GatewayID:      response.GatewayId,
				ChainPEM:       response.CertificateChainPem,
				TrustBundlePEM: response.TrustBundlePem,
				AuthorityID:    response.AuthorityId,
				Serial:         response.SerialNumber,
				NotBefore:      response.NotBeforeUnixMs,
				NotAfter:       response.NotAfterUnixMs,
			})
		}
		if !retryable(status.Code(callErr)) {
			return errors.New("control client: enrollment rejected")
		}
		backoff := time.Duration(100*(1<<min(attempt, 5))) * time.Millisecond
		jitter := time.Duration(rand.IntN(100)) * time.Millisecond
		timer := time.NewTimer(backoff + jitter)
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

// Retryable reports whether an RPC failure is safe to retry while the
// incumbent credential remains valid.
func Retryable(err error) bool { return retryable(status.Code(err)) }

func (c *Client) connectAuthenticated(ctx context.Context) error {
	conn, err := c.newAuthenticatedConn()
	if err != nil {
		return err
	}
	c.conn = conn
	return nil
}

func (c *Client) newAuthenticatedConn() (*grpc.ClientConn, error) {
	tlsConfig, err := pki.NewAPIPeerTLSConfig(c.cfg.Identity.BootstrapCA(), time.Now)
	if err != nil {
		return nil, err
	}
	tlsConfig.GetClientCertificate = c.cfg.Identity.GetClientCertificate
	conn, err := grpc.NewClient(c.cfg.Target, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (c *Client) Conn() *grpc.ClientConn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}

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
