package apiidentity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki"
)

type testSigner struct {
	mu                 sync.Mutex
	now                time.Time
	ttl                time.Duration
	fail               error
	entered            chan struct{}
	resume             chan struct{}
	calls              int
	root               *x509.Certificate
	rootKey            ed25519.PrivateKey
	issuer             *x509.Certificate
	issuerKey          ed25519.PrivateKey
	rootPEM, issuerPEM []byte
}

func newTestSigner(t *testing.T, now time.Time, ttl time.Duration) *testSigner {
	t.Helper()
	rootPub, rootKey, _ := ed25519.GenerateKey(rand.Reader)
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, MaxPathLen: 1, KeyUsage: x509.KeyUsageCertSign}
	rootDER, _ := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, rootPub, rootKey)
	root, _ := x509.ParseCertificate(rootDER)
	issuerPub, issuerKey, _ := ed25519.GenerateKey(rand.Reader)
	issuerTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "issuer"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(36 * time.Hour), IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign}
	issuerDER, _ := x509.CreateCertificate(rand.Reader, issuerTemplate, root, issuerPub, rootKey)
	issuer, _ := x509.ParseCertificate(issuerDER)
	return &testSigner{now: now, ttl: ttl, root: root, rootKey: rootKey, issuer: issuer, issuerKey: issuerKey, rootPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}), issuerPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuerDER})}
}

func (s *testSigner) SignAPI(ctx context.Context, request pki.APISignRequest) (pki.SignedCertificate, error) {
	s.mu.Lock()
	s.calls++
	fail, entered, resume, now, ttl := s.fail, s.entered, s.resume, s.now, s.ttl
	s.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if resume != nil {
		select {
		case <-ctx.Done():
			return pki.SignedCertificate{}, ctx.Err()
		case <-resume:
		}
	}
	if fail != nil {
		return pki.SignedCertificate{}, fail
	}
	policy, _ := pki.NewPolicy(ttl, time.Minute)
	template, err := pki.NewAPILeafTemplate(request.PublicKey, policy, now, s.issuer.NotAfter, rand.Reader)
	if err != nil {
		return pki.SignedCertificate{}, err
	}
	der, err := x509.CreateCertificate(rand.Reader, template, s.issuer, request.PublicKey, s.issuerKey)
	if err != nil {
		return pki.SignedCertificate{}, err
	}
	leaf, _ := x509.ParseCertificate(der)
	fingerprint := sha256.Sum256(der)
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), s.issuerPEM...)
	return pki.SignedCertificate{DER: der, ChainPEM: chain, TrustBundlePEM: s.rootPEM, Fingerprint: fingerprint[:], AuthorityID: "issuer", Serial: leaf.SerialNumber, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter}, nil
}

func TestIssueReloadAndPermissions(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	directory := filepath.Join(t.TempDir(), "identity")
	signer := newTestSigner(t, now, 12*time.Hour)
	m, err := New(Config{Directory: directory, RenewBefore: time.Hour}, signer)
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return now }
	if err = m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := m.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := readCurrent(directory)
	for path, mode := range map[string]os.FileMode{directory: 0o700, filepath.Join(directory, current): 0o700, filepath.Join(directory, current, "key.pk8"): 0o600, filepath.Join(directory, current, "chain.pem"): 0o644, filepath.Join(directory, current, "trust.pem"): 0o644, filepath.Join(directory, current, "metadata.json"): 0o644} {
		if err = requireMode(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, _ := New(Config{Directory: directory, RenewBefore: time.Hour}, signer)
	reloaded.now = func() time.Time { return now }
	if err = reloaded.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, _ := reloaded.GetCertificate(nil)
	if !bytes.Equal(first.Certificate[0], second.Certificate[0]) || signer.calls != 1 {
		t.Fatal("reload issued a new key or certificate")
	}
}

func TestCorruptionAndAtomicRecovery(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	directory := filepath.Join(t.TempDir(), "identity")
	signer := newTestSigner(t, now, 4*time.Hour)
	m, _ := New(Config{Directory: directory, RenewBefore: 2 * time.Hour}, signer)
	m.now = func() time.Time { return now }
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstGeneration, _ := readCurrent(directory)
	signer.mu.Lock()
	signer.now = now.Add(3 * time.Hour)
	signer.mu.Unlock()
	m.now = func() time.Time { return now.Add(3 * time.Hour) }
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	secondGeneration, _ := readCurrent(directory)
	if firstGeneration == secondGeneration {
		t.Fatal("rotation did not publish generation")
	}
	if err := os.WriteFile(filepath.Join(directory, secondGeneration, "chain.pem"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".current.tmp"), []byte("identity-incomplete\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "current"), []byte("identity-incomplete\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recovered, _ := New(Config{Directory: directory, RenewBefore: 30 * time.Minute}, signer)
	recovered.now = func() time.Time { return now.Add(3 * time.Hour) }
	if err := recovered.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _ := readCurrent(directory)
	if current != firstGeneration || !recovered.Ready() {
		t.Fatalf("did not recover previous generation: %s", current)
	}
}

func TestIncompleteNewerGenerationNeverDisplacesValidatedPrevious(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	directory := filepath.Join(t.TempDir(), "identity")
	signer := newTestSigner(t, now, 6*time.Hour)
	m, _ := New(Config{Directory: directory, RenewBefore: 3 * time.Hour}, signer)
	m.now = func() time.Time { return now }
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	t1, _ := readCurrent(directory)
	t1Key, _ := os.ReadFile(filepath.Join(directory, t1, "key.pk8"))
	t1Cert, _ := m.GetCertificate(nil)
	t2 := "identity-ZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	if err := os.Mkdir(filepath.Join(directory, t2), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, t2, "metadata.json"), []byte("incomplete"), 0o644); err != nil {
		t.Fatal(err)
	}
	signer.mu.Lock()
	signer.now = now.Add(4 * time.Hour)
	signer.mu.Unlock()
	m.now = func() time.Time { return now.Add(4 * time.Hour) }
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	t3, _ := readCurrent(directory)
	if t3 == t1 {
		t.Fatal("T3 was not published")
	}
	if _, err := os.Stat(filepath.Join(directory, t2)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete T2 was retained as previous")
	}
	if err := os.WriteFile(filepath.Join(directory, t3, "chain.pem"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := New(Config{Directory: directory, RenewBefore: time.Hour}, signer)
	reloaded.now = func() time.Time { return now.Add(4 * time.Hour) }
	if err := reloaded.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _ := readCurrent(directory)
	if current != t1 {
		t.Fatalf("recovered %s, want T1 %s", current, t1)
	}
	recoveredKey, _ := os.ReadFile(filepath.Join(directory, t1, "key.pk8"))
	recoveredCert, _ := reloaded.GetCertificate(nil)
	if !bytes.Equal(t1Key, recoveredKey) || !bytes.Equal(t1Cert.Certificate[0], recoveredCert.Certificate[0]) {
		t.Fatal("T1 key/certificate changed during recovery")
	}
	if _, err := os.Stat(filepath.Join(directory, t3)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("corrupt T3 was not removed")
	}
}

func TestMismatchFailureAndExpiry(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	directory := filepath.Join(t.TempDir(), "identity")
	signer := newTestSigner(t, now, 2*time.Hour)
	m, _ := New(Config{Directory: directory, RenewBefore: time.Hour}, signer)
	m.now = func() time.Time { return now }
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	incumbent, _ := m.GetCertificate(nil)
	signer.mu.Lock()
	signer.fail = errors.New("CA unavailable")
	signer.mu.Unlock()
	m.now = func() time.Time { return now.Add(90 * time.Minute) }
	if err := m.Ensure(context.Background()); err == nil || !m.Ready() {
		t.Fatal("renewal failure did not retain valid incumbent")
	}
	still, _ := m.GetCertificate(nil)
	if !bytes.Equal(incumbent.Certificate[0], still.Certificate[0]) {
		t.Fatal("incumbent changed on failure")
	}
	m.now = func() time.Time { return now.Add(3 * time.Hour) }
	if m.Ready() {
		t.Fatal("expired identity remained ready")
	}
	if _, err := m.GetCertificate(nil); err == nil {
		t.Fatal("expired certificate returned")
	}

	current, _ := readCurrent(directory)
	keyPath := filepath.Join(directory, current, "key.pk8")
	_, foreign, _ := ed25519.GenerateKey(rand.Reader)
	foreignDER, _ := x509.MarshalPKCS8PrivateKey(foreign)
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: foreignDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	broken, _ := New(Config{Directory: directory, RenewBefore: time.Hour}, signer)
	broken.now = func() time.Time { return now }
	if err := broken.Ensure(context.Background()); err == nil || broken.Ready() {
		t.Fatal("mismatched key loaded")
	}
}

func TestConcurrentGetCertificateDuringRotation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	signer := newTestSigner(t, now, 3*time.Hour)
	m, _ := New(Config{Directory: filepath.Join(t.TempDir(), "identity"), RenewBefore: 2 * time.Hour}, signer)
	m.now = func() time.Time { return now }
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	incumbent, _ := m.GetCertificate(nil)
	signer.mu.Lock()
	signer.now = now.Add(2 * time.Hour)
	signer.entered = make(chan struct{}, 1)
	signer.resume = make(chan struct{})
	signer.mu.Unlock()
	m.now = func() time.Time { return now.Add(2 * time.Hour) }
	done := make(chan error, 1)
	go func() { done <- m.Ensure(context.Background()) }()
	<-signer.entered
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			cert, err := m.GetCertificate(&tls.ClientHelloInfo{})
			if err != nil || !bytes.Equal(cert.Certificate[0], incumbent.Certificate[0]) {
				t.Errorf("incumbent unavailable during rotation")
			}
		}()
	}
	wait.Wait()
	close(signer.resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rotated, _ := m.GetCertificate(nil)
	if bytes.Equal(rotated.Certificate[0], incumbent.Certificate[0]) {
		t.Fatal("rotation did not swap certificate")
	}
}
