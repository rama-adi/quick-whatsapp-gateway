package localmysql

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	base "github.com/rama-adi/quick-whatsapp-gateway/internal/pki"
)

type failingEntropy struct{}

func (failingEntropy) Read([]byte) (int, error) { return 0, errors.New("entropy failure") }

// TestMySQLHierarchy exercises the transaction and persistence behavior against a migrated,
// disposable MySQL database when PKI_MYSQL_TEST_DSN is provided by CI or a developer.
func TestMySQLHierarchy(t *testing.T) {
	dsn := os.Getenv("PKI_MYSQL_TEST_DSN")
	if dsn == "" || os.Getenv("PKI_MYSQL_TEST_DISPOSABLE") != "1" {
		t.Skip("disposable MySQL test requires PKI_MYSQL_TEST_DSN and PKI_MYSQL_TEST_DISPOSABLE=1")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	var schema string
	if err = db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(schema, "qwg_pki_test") {
		t.Fatalf("refusing destructive cleanup of non-disposable schema %q", schema)
	}
	if _, err = db.ExecContext(ctx, "DELETE FROM gateway_certificates"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"intermediate", "root"} {
		if _, err = db.ExecContext(ctx, "DELETE FROM pki_authorities WHERE kind=?", kind); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := base.NewPolicy(10*time.Minute, time.Minute)
	cfg := Config{KEK: bytes.Repeat([]byte{3}, 32), KeyID: "mysql-test", RootTTL: 24 * time.Hour, IntermediateTTL: 2 * time.Hour, RenewBefore: 30 * time.Minute, Policy: p}
	first, err := New(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Now().UTC().Truncate(time.Millisecond)
	first.now = func() time.Time { return fixed }
	second, _ := New(db, cfg)
	second.now = first.now
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	for _, s := range []*Signer{first, second} {
		go func(s *Signer) { defer wg.Done(); errs <- s.EnsureHierarchy(ctx) }(s)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var roots, intermediates int
	if err = db.QueryRowContext(ctx, "SELECT SUM(kind='root'),SUM(kind='intermediate') FROM pki_authorities WHERE status='active'").Scan(&roots, &intermediates); err != nil {
		t.Fatal(err)
	}
	if roots != 1 || intermediates != 1 {
		t.Fatalf("active hierarchy = roots %d intermediates %d", roots, intermediates)
	}
	var before []byte
	if err = db.QueryRowContext(ctx, "SELECT certificate_fingerprint FROM pki_authorities WHERE kind='root' AND status='active'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	restart, _ := New(db, cfg)
	restart.now = first.now
	if err = restart.EnsureHierarchy(ctx); err != nil {
		t.Fatal(err)
	}
	var after []byte
	_ = db.QueryRowContext(ctx, "SELECT certificate_fingerprint FROM pki_authorities WHERE kind='root' AND status='active'").Scan(&after)
	if !bytes.Equal(before, after) {
		t.Fatal("restart replaced root")
	}
	renewAt := func() time.Time { return fixed.Add(100 * time.Minute) }
	restart.now = renewAt
	renewer2, _ := New(db, cfg)
	renewer2.now = renewAt
	errs = make(chan error, 2)
	wg.Add(2)
	for _, s := range []*Signer{restart, renewer2} {
		go func(s *Signer) { defer wg.Done(); errs <- s.EnsureHierarchy(ctx) }(s)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var history int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pki_authorities WHERE kind='intermediate'").Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history != 2 {
		t.Fatalf("intermediate history=%d", history)
	}
	// The persisted issuer signs a verifiable SPIFFE leaf and returns its chain.
	_, leafKey, _ := ed25519.GenerateKey(rand.Reader)
	uri, _ := url.Parse("spiffe://quick-wa/gateway/mysql_gw")
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "mysql_gw"}, URIs: []*url.URL{uri}}, leafKey)
	csr, _ := base.ValidateCSR(csrDER, "mysql_gw")
	issued, err := restart.SignGateway(ctx, base.SignRequest{GatewayID: "mysql_gw", CSR: csr})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(issued.DER)
	if err != nil || leaf.CheckSignatureFrom(restart.cache.intermediateCert) != nil {
		t.Fatal("persisted leaf chain did not verify")
	}
	apiPublicKey, _, _ := ed25519.GenerateKey(rand.Reader)
	apiIdentity, err := restart.SignAPI(ctx, base.APISignRequest{PublicKey: apiPublicKey})
	if err != nil {
		t.Fatal(err)
	}
	if err = base.ValidateSignedAPI(apiIdentity, apiPublicKey, renewAt()); err != nil {
		t.Fatalf("persisted API identity did not verify: %v", err)
	}
	// A successor-generation failure occurs before retirement and leaves the incumbent active.
	restart.now = func() time.Time { return fixed.Add(191 * time.Minute) }
	restart.random = failingEntropy{}
	if err = restart.EnsureHierarchy(ctx); err == nil {
		t.Fatal("candidate failure ignored")
	}
	var active, afterFailure int
	_ = db.QueryRowContext(ctx, "SELECT SUM(status='active'),COUNT(*) FROM pki_authorities WHERE kind='intermediate'").Scan(&active, &afterFailure)
	if active != 1 || afterFailure != 2 {
		t.Fatal("candidate failure mutated hierarchy")
	}
	restart.random = rand.Reader
	// Corruption fails closed and does not silently replace the row.
	var activeID, certificatePEM string
	_ = db.QueryRowContext(ctx, "SELECT id,certificate_pem FROM pki_authorities WHERE kind='intermediate' AND status='active'").Scan(&activeID, &certificatePEM)
	_, _ = db.ExecContext(ctx, "UPDATE pki_authorities SET certificate_pem='corrupt' WHERE id=?", activeID)
	restart.now = renewAt
	if err = restart.EnsureHierarchy(ctx); err == nil {
		t.Fatal("corrupt row accepted")
	}
	_, _ = db.ExecContext(ctx, "UPDATE pki_authorities SET certificate_pem=? WHERE id=?", certificatePEM, activeID)
	restart.now = func() time.Time { return fixed.Add(25 * time.Hour) }
	if err = restart.EnsureHierarchy(ctx); !errors.Is(err, ErrRootRotationRequired) {
		t.Fatalf("expired root error=%v", err)
	}
}
