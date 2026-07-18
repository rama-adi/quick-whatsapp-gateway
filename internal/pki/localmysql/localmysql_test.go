package localmysql

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	base "github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
)

type memoryAuthorities struct {
	mu         sync.Mutex
	rows       []domain.PKIAuthority
	failInsert bool
}
type memoryTx struct {
	owner *memoryAuthorities
	rows  []domain.PKIAuthority
}

func (m *memoryAuthorities) WithHierarchyLock(_ context.Context, fn func(AuthorityRepository) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memoryTx{owner: m, rows: append([]domain.PKIAuthority(nil), m.rows...)}
	if err := fn(tx); err != nil {
		return err
	}
	m.rows = tx.rows
	return nil
}
func (m *memoryTx) LoadActive(_ context.Context, kind string) (domain.PKIAuthority, error) {
	for i := len(m.rows) - 1; i >= 0; i-- {
		if m.rows[i].Kind == kind && m.rows[i].Status == "active" {
			return m.rows[i], nil
		}
	}
	return domain.PKIAuthority{}, sql.ErrNoRows
}
func (m *memoryTx) Insert(_ context.Context, a domain.PKIAuthority) error {
	if m.owner.failInsert {
		return errors.New("insert")
	}
	m.rows = append(m.rows, a)
	return nil
}
func (m *memoryTx) RetireActiveIntermediate(_ context.Context, now int64) error {
	for i := range m.rows {
		if m.rows[i].Kind == "intermediate" && m.rows[i].Status == "active" {
			m.rows[i].Status = "retiring"
			m.rows[i].UpdatedAt = now
		}
	}
	return nil
}

func testSigner(t *testing.T) *Signer {
	t.Helper()
	p, err := base.NewPolicy(12*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s := &Signer{cfg: Config{KEK: bytes.Repeat([]byte{7}, 32), KeyID: "test-kek", RootTTL: 365 * 24 * time.Hour, IntermediateTTL: 30 * 24 * time.Hour, RenewBefore: 7 * 24 * time.Hour, Policy: p}, now: time.Now, random: rand.Reader}
	return s
}

func TestEnsureHierarchyBootstrapRestartRotationAndRollback(t *testing.T) {
	s := testSigner(t)
	repo := &memoryAuthorities{}
	s.repo = repo
	fixed := time.Now().UTC().Truncate(time.Millisecond)
	s.now = func() time.Time { return fixed }
	if err := s.EnsureHierarchy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.rows) != 2 {
		t.Fatalf("bootstrap rows=%d", len(repo.rows))
	}
	rootFP := append([]byte(nil), repo.rows[0].CertificateFingerprint...)
	restart := testSigner(t)
	restart.repo = repo
	restart.now = s.now
	if err := restart.EnsureHierarchy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.rows) != 2 || !bytes.Equal(rootFP, repo.rows[0].CertificateFingerprint) {
		t.Fatal("restart did not reuse hierarchy")
	}
	restart.now = func() time.Time { return fixed.Add(24 * 24 * time.Hour) } // inside the seven-day renewal window
	repo.failInsert = true
	if err := restart.EnsureHierarchy(context.Background()); err == nil {
		t.Fatal("insert failure ignored")
	}
	if len(repo.rows) != 2 || repo.rows[1].Status != "active" {
		t.Fatal("failed renewal did not roll back")
	}
	repo.failInsert = false
	if err := restart.EnsureHierarchy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.rows) != 3 || repo.rows[1].Status != "retiring" || repo.rows[2].Status != "active" {
		t.Fatal("renewal history incorrect")
	}
	rowsBefore := len(repo.rows)
	restart.now = func() time.Time {
		return fixed.Add(365*24*time.Hour - restart.cfg.Policy.TTL - restart.cfg.Policy.Skew + time.Millisecond)
	}
	if err := restart.EnsureHierarchy(context.Background()); !errors.Is(err, ErrRootRotationRequired) {
		t.Fatalf("near-expiry root error=%v", err)
	}
	if len(repo.rows) != rowsBefore {
		t.Fatal("near-expiry root mutated hierarchy")
	}
	restart.now = func() time.Time { return fixed.Add(366 * 24 * time.Hour) }
	if err := restart.EnsureHierarchy(context.Background()); !errors.Is(err, ErrRootRotationRequired) {
		t.Fatalf("expired root error=%v", err)
	}
}

func TestHierarchyPersistenceValidationAndSigning(t *testing.T) {
	s := testSigner(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	root, err := s.createRoot(now)
	if err != nil {
		t.Fatal(err)
	}
	intermediate, err := s.createIntermediate(now, root)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(root.EncryptedPrivateKey, []byte("PRIVATE KEY")) || bytes.Contains(intermediate.EncryptedPrivateKey, []byte("PRIVATE KEY")) {
		t.Fatal("plaintext key persisted")
	}
	if err := s.install(root, intermediate, now); err != nil {
		t.Fatal(err)
	}
	bundle, err := s.TrustBundle()
	if err != nil || string(bundle) != root.CertificatePEM {
		t.Fatal("trust bundle is not exact root")
	}

	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	u, _ := url.Parse("spiffe://quick-wa/gateway/gw_1")
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "gw_1"}, URIs: []*url.URL{u}}, key)
	csr, err := base.ValidateCSR(csrDER, "gw_1")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.Sign(context.Background(), base.SignRequest{GatewayID: "gw_1", CSR: csr})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(issued.DER)
	if err != nil {
		t.Fatal(err)
	}
	if !leaf.PublicKey.(ed25519.PublicKey).Equal(pub) || leaf.CheckSignatureFrom(s.cache.intermediateCert) != nil || len(leaf.URIs) != 1 || leaf.URIs[0].String() != u.String() {
		t.Fatal("invalid leaf chain or identity")
	}
	if len(leaf.ExtKeyUsage) != 2 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || leaf.ExtKeyUsage[1] != x509.ExtKeyUsageServerAuth {
		t.Fatal("invalid leaf EKUs")
	}
	fp := sha256.Sum256(issued.DER)
	if !bytes.Equal(fp[:], issued.Fingerprint) || issued.AuthorityID != intermediate.ID {
		t.Fatal("issuance metadata mismatch")
	}
	if !bytes.Contains(issued.ChainPEM, []byte(intermediate.CertificatePEM)) || string(issued.TrustBundlePEM) != root.CertificatePEM {
		t.Fatal("chain material mismatch")
	}
}

func TestRowsFailClosed(t *testing.T) {
	s := testSigner(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	root, _ := s.createRoot(now)
	tests := []func(*Signer, *domainRow){
		func(_ *Signer, r *domainRow) { r.a.CertificatePEM = "corrupt" },
		func(_ *Signer, r *domainRow) { r.a.CertificateFingerprint[0] ^= 1 },
		func(_ *Signer, r *domainRow) { r.a.EncryptedPrivateKey[0] ^= 1 },
		func(x *Signer, _ *domainRow) { x.cfg.KEK = bytes.Repeat([]byte{9}, 32) },
		func(_ *Signer, r *domainRow) { r.a.ID = "substituted" },
		func(_ *Signer, r *domainRow) { r.a.EncryptionKeyID = "other-kek" },
	}
	for i, mutate := range tests {
		copyRow := root
		copyRow.CertificateFingerprint = append([]byte(nil), root.CertificateFingerprint...)
		copyRow.EncryptedPrivateKey = append([]byte(nil), root.EncryptedPrivateKey...)
		r := domainRow{a: copyRow}
		x := &Signer{cfg: s.cfg, now: s.now, random: s.random}
		mutate(x, &r)
		if _, _, err := x.validate(r.a, now); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

type domainRow struct{ a domain.PKIAuthority }

func TestKeyCertificateMismatchAndCSRRevalidation(t *testing.T) {
	s := testSigner(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	r1, _ := s.createRoot(now)
	r2, _ := s.createRoot(now)
	r1.EncryptedPrivateKey = r2.EncryptedPrivateKey
	r1.PrivateKeyNonce = r2.PrivateKeyNonce
	// Rebinding ciphertext to another row is rejected by AAD before a mismatched key can be used.
	if _, _, err := s.validate(r1, now); err == nil {
		t.Fatal("key substitution accepted")
	}
	root, _ := s.createRoot(now)
	intermediate, _ := s.createIntermediate(now, root)
	if err := s.install(root, intermediate, now); err != nil {
		t.Fatal(err)
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	u, _ := url.Parse("spiffe://quick-wa/gateway/gw")
	der, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{URIs: []*url.URL{u}}, key)
	csr, _ := base.ValidateCSR(der, "gw")
	bad := base.SignRequest{GatewayID: "other", CSR: csr}
	if _, err := s.Sign(context.Background(), bad); err == nil {
		t.Fatal("CSR binding not revalidated")
	}
	s.now = func() time.Time { return now.Add(31 * 24 * time.Hour) }
	if _, err := s.Sign(context.Background(), base.SignRequest{GatewayID: "gw", CSR: csr}); err == nil {
		t.Fatal("stale request backdated issuer validity")
	}
}

func TestAuthorityConstraints(t *testing.T) {
	s := testSigner(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	root, _ := s.createRoot(now)
	intermediate, _ := s.createIntermediate(now, root)
	rb, _ := pem.Decode([]byte(root.CertificatePEM))
	ib, _ := pem.Decode([]byte(intermediate.CertificatePEM))
	rc, _ := x509.ParseCertificate(rb.Bytes)
	ic, _ := x509.ParseCertificate(ib.Bytes)
	if rc.MaxPathLen != 1 || !rc.IsCA || ic.MaxPathLen != 0 || !ic.MaxPathLenZero || ic.CheckSignatureFrom(rc) != nil {
		t.Fatal("invalid authority constraints")
	}
}
