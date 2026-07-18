package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki/apiidentity"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki/localmysql"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

func TestPrivateGatewayTLSMySQLIntegration(t *testing.T) {
	dsn := os.Getenv("API_PRIVATE_TLS_TEST_DSN")
	if dsn == "" || os.Getenv("API_PRIVATE_TLS_TEST_DISPOSABLE") != "1" {
		t.Skip("disposable MySQL test requires API_PRIVATE_TLS_TEST_DSN and API_PRIVATE_TLS_TEST_DISPOSABLE=1")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var schema string
	if err = db.QueryRow("SELECT DATABASE()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(schema, "qwg_private_tls_test") {
		t.Fatalf("refusing integration test against schema %q", schema)
	}
	for _, statement := range []string{"DELETE FROM audit_events", "DELETE FROM gateway_certificates", "DELETE FROM gateway_enrollment_tokens", "DELETE FROM gateways", "DELETE FROM pki_authorities"} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	policy, _ := pki.NewPolicy(20*time.Minute, time.Minute)
	signer, err := localmysql.New(db, localmysql.Config{KEK: bytes.Repeat([]byte{4}, 32), KeyID: "tls-integration", RootTTL: 48 * time.Hour, IntermediateTTL: 4 * time.Hour, RenewBefore: time.Hour, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if err = signer.EnsureHierarchy(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager, err := apiidentity.New(apiidentity.Config{Directory: t.TempDir(), RenewBefore: 5 * time.Minute}, signer)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	bundle, _ := signer.TrustBundle()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(bundle) {
		t.Fatal("invalid root bundle")
	}
	enrollment, err := service.NewEnrollmentService(db, signer, service.DefaultEnrollmentConfig())
	if err != nil {
		t.Fatal(err)
	}
	issued, err := enrollment.CreateGateway(context.Background(), service.CreateGatewayInput{CreatedByUserID: "integration-user"})
	if err != nil {
		t.Fatal(err)
	}

	server := newPrivateGatewayGRPCServer(privateGatewayTLSConfig(manager, roots), privateGatewayAuthenticator{store: mysqlGatewayCredentialStore{db: db}}, enrollment, func() error { return nil })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	defer func() { server.Stop(); <-serveDone }()

	clientTLS := apiClientTLS(t, roots)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	if err != nil {
		t.Fatal(err)
	}
	client := gatewayv1.NewGatewayEnrollmentServiceClient(conn)
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	u, _ := url.Parse("spiffe://quick-wa/gateway/" + issued.GatewayID)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: issued.GatewayID}, URIs: []*url.URL{u}}, private)
	response, err := client.Enroll(context.Background(), &gatewayv1.GatewayEnrollmentServiceEnrollRequest{Token: issued.Token, CsrDer: csr})
	_ = conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	leafBlock, _ := pem.Decode(response.CertificateChainPem)
	leaf, _ := x509.ParseCertificate(leafBlock.Bytes)
	if !leaf.PublicKey.(ed25519.PublicKey).Equal(public) {
		t.Fatal("issued leaf key mismatch")
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(private)
	clientCert, err := tls.X509KeyPair(response.CertificateChainPem, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	clientTLS.Certificates = []tls.Certificate{clientCert}
	conn, err = grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	health := gatewayv1.NewGatewayHealthServiceClient(conn)
	if _, err = health.Check(context.Background(), &gatewayv1.GatewayHealthServiceCheckRequest{}); err != nil {
		t.Fatalf("authenticated health: %v", err)
	}
	if _, err = db.Exec("UPDATE gateways SET status='disabled' WHERE id=?", issued.GatewayID); err != nil {
		t.Fatal(err)
	}
	if _, err = health.Check(context.Background(), &gatewayv1.GatewayHealthServiceCheckRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("disabled gateway = %v", err)
	}
	if _, err = db.Exec("UPDATE gateways SET status='active' WHERE id=?", issued.GatewayID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE gateway_certificates SET revoked_at=?,revocation_reason='integration' WHERE gateway_id=?", time.Now().UnixMilli(), issued.GatewayID); err != nil {
		t.Fatal(err)
	}
	if _, err = health.Check(context.Background(), &gatewayv1.GatewayHealthServiceCheckRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("revoked certificate = %v", err)
	}
}

func apiClientTLS(t *testing.T, roots *x509.CertPool) *tls.Config {
	t.Helper()
	return &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, VerifyConnection: func(state tls.ConnectionState) error { // #nosec G402 -- custom SPIFFE verification below replaces hostname verification.
		intermediates := x509.NewCertPool()
		for _, cert := range state.PeerCertificates[1:] {
			intermediates.AddCert(cert)
		}
		_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
		if err != nil {
			return err
		}
		if len(state.PeerCertificates[0].URIs) != 1 || state.PeerCertificates[0].URIs[0].String() != pki.APIIdentityURI {
			return errors.New("unexpected API SPIFFE identity")
		}
		return nil
	}}
}
