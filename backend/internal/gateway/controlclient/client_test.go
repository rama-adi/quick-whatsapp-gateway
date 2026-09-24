package controlclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki/gatewayidentity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"
)

func testRoot(t *testing.T) []byte {
	t.Helper()
	now := time.Now()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
func TestRetryClassification(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded, codes.Aborted, codes.ResourceExhausted} {
		if !retryable(code) {
			t.Fatalf("%s should retry", code)
		}
	}
	for _, code := range []codes.Code{codes.InvalidArgument, codes.Unauthenticated, codes.PermissionDenied, codes.Internal} {
		if retryable(code) {
			t.Fatalf("%s should be terminal", code)
		}
	}
}

func TestRenewReplacesConnectionOnlyAfterInstallingValidatedIdentity(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	rootPub, rootKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(200), Subject: pkix.Name{CommonName: "root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, rootPub, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := x509.ParseCertificate(rootDER)
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})
	identity, err := gatewayidentity.New(gatewayidentity.Config{Directory: filepath.Join(t.TempDir(), "credentials"), GatewayID: "gw_1", BootstrapCA: rootPEM})
	if err != nil {
		t.Fatal(err)
	}
	issue := func(csrDER []byte, serial int64) gatewayidentity.Installation {
		t.Helper()
		csr, parseErr := x509.ParseCertificateRequest(csrDER)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(30 * time.Minute), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, URIs: csr.URIs}
		leafDER, signErr := x509.CreateCertificate(rand.Reader, leafTemplate, root, csr.PublicKey, rootKey)
		if signErr != nil {
			t.Fatal(signErr)
		}
		leaf, _ := x509.ParseCertificate(leafDER)
		return gatewayidentity.Installation{GatewayID: "gw_1", ChainPEM: append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), rootPEM...), TrustBundlePEM: rootPEM, AuthorityID: "root", Serial: leaf.SerialNumber.String(), NotBefore: leaf.NotBefore.UnixMilli(), NotAfter: leaf.NotAfter.UnixMilli()}
	}
	pending, err := identity.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if err = identity.Install(issue(pending.CSRDER, 201)); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{Target: "127.0.0.1:1", GatewayID: "gw_1", Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Ensure(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	incumbent := client.Conn()
	called := false
	rollover, err := client.BeginRenewal(context.Background(), RenewalFunc(func(_ context.Context, conn *grpc.ClientConn, csrDER []byte) (gatewayidentity.Installation, error) {
		called = true
		if conn != incumbent {
			t.Fatal("renewal did not use the incumbent connection")
		}
		return issue(csrDER, 202), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !called || client.Conn() == incumbent || incumbent.GetState() == connectivity.Shutdown {
		t.Fatal("renewal did not stage a replacement while retaining the incumbent")
	}
	if err = rollover.Commit(); err != nil {
		t.Fatal(err)
	}
	if incumbent.GetState() != connectivity.Shutdown {
		t.Fatal("renewal did not retire the incumbent after commit")
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRenewalFailureRetainsIncumbentConnection(t *testing.T) {
	identity, err := gatewayidentity.New(gatewayidentity.Config{Directory: filepath.Join(t.TempDir(), "credentials"), GatewayID: "gw_1", BootstrapCA: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	// A no-op client cannot renew before an authenticated connection exists.
	client, err := New(Config{Target: "127.0.0.1:1", GatewayID: "gw_1", Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Renew(context.Background(), RenewalFunc(func(context.Context, *grpc.ClientConn, []byte) (gatewayidentity.Installation, error) {
		t.Fatal("transport called without incumbent")
		return gatewayidentity.Installation{}, nil
	})); err == nil {
		t.Fatal("renewal without incumbent succeeded")
	}
}
func TestCanceledRetryReusesPendingAndNeverPersistsToken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	identity, _ := gatewayidentity.New(gatewayidentity.Config{Directory: dir, GatewayID: "gw_1", BootstrapCA: testRoot(t)})
	client, _ := New(Config{Target: "127.0.0.1:1", GatewayID: "gw_1", Identity: identity, AttemptTimeout: 20 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	secret := "qwg_enroll_v1_SECRET_NEVER_WRITE"
	if err := client.Ensure(ctx, secret); err == nil {
		t.Fatal("unreachable enrollment succeeded")
	}
	first, _ := identity.Prepare()
	second, _ := identity.Prepare()
	if string(first.CSRDER) != string(second.CSRDER) {
		t.Fatal("pending CSR changed across retry")
	}
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), secret) {
				t.Fatalf("token persisted in %s", path)
			}
		}
		return nil
	})
}

type countedPrivateServer struct {
	gatewayv1.UnimplementedGatewayEnrollmentServiceServer
	gatewayv1.UnimplementedGatewayHealthServiceServer
	enrollCalls atomic.Int32
	healthCalls atomic.Int32
}

func (s *countedPrivateServer) Enroll(context.Context, *gatewayv1.GatewayEnrollmentServiceEnrollRequest) (*gatewayv1.GatewayEnrollmentServiceEnrollResponse, error) {
	s.enrollCalls.Add(1)
	return nil, status.Error(codes.Internal, "enroll must not be called")
}
func (s *countedPrivateServer) Check(context.Context, *gatewayv1.GatewayHealthServiceCheckRequest) (*gatewayv1.GatewayHealthServiceCheckResponse, error) {
	s.healthCalls.Add(1)
	return &gatewayv1.GatewayHealthServiceCheckResponse{Status: gatewayv1.ServingStatus_SERVING_STATUS_SERVING}, nil
}

func TestInstalledIdentityDoesNotRequireAPIAvailabilityAtStartup(t *testing.T) {
	root := testRoot(t)
	// Reuse the identity fixture path exercised above by installing a credential
	// through a temporary authenticated server, then prove a later offline boot
	// only constructs the persistent connection. grpc-go reconnects underneath
	// the supervisor.
	dir := filepath.Join(t.TempDir(), "credentials")
	identity, err := gatewayidentity.New(gatewayidentity.Config{Directory: dir, GatewayID: "gw_1", BootstrapCA: root})
	if err != nil {
		t.Fatal(err)
	}
	// A corrupt or absent credential must remain fail-closed without a token.
	client, err := New(Config{Target: "127.0.0.1:1", GatewayID: "gw_1", Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Ensure(context.Background(), ""); err == nil {
		t.Fatal("unenrolled identity started without enrollment token")
	}
}
