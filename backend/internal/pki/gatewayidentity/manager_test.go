package gatewayidentity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixture(t *testing.T) ([]byte, *x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	now := time.Now().UTC()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	cert, _ := x509.ParseCertificate(der)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert, key
}
func installation(t *testing.T, p Pending, ca []byte, root *x509.Certificate, key ed25519.PrivateKey) Installation {
	t.Helper()
	csr, _ := x509.ParseCertificateRequest(p.CSRDER)
	now := time.Now().UTC().Truncate(time.Millisecond)
	template := &x509.Certificate{SerialNumber: big.NewInt(8), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, URIs: csr.URIs}
	der, _ := x509.CreateCertificate(rand.Reader, template, root, csr.PublicKey, key)
	leaf, _ := x509.ParseCertificate(der)
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), ca...)
	return Installation{GatewayID: "gw_1", ChainPEM: chain, TrustBundlePEM: ca, AuthorityID: "root", Serial: leaf.SerialNumber.String(), NotBefore: leaf.NotBefore.UnixMilli(), NotAfter: leaf.NotAfter.UnixMilli()}
}
func TestPendingReuseInstallReloadAndPermissions(t *testing.T) {
	ca, root, key := fixture(t)
	dir := filepath.Join(t.TempDir(), "credentials")
	m, _ := New(Config{Directory: dir, GatewayID: "gw_1", BootstrapCA: ca})
	first, err := m.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Prepare()
	if err != nil || !bytes.Equal(first.CSRDER, second.CSRDER) {
		t.Fatal("pending CSR not reused")
	}
	if mode, _ := os.Stat(dir); mode.Mode().Perm() != 0o700 {
		t.Fatalf("root mode %o", mode.Mode().Perm())
	}
	if mode, _ := os.Stat(filepath.Join(dir, "pending", "key.pem")); mode.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %o", mode.Mode().Perm())
	}
	if err = m.Install(installation(t, first, ca, root, key)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "pending")); !os.IsNotExist(err) {
		t.Fatal("pending retained after publish")
	}
	if !m.Ready() {
		t.Fatal("installed identity not ready")
	}
	reloaded, _ := New(Config{Directory: dir, GatewayID: "gw_1", BootstrapCA: ca})
	if err = reloaded.Load(); err != nil || !reloaded.Ready() {
		t.Fatalf("reload: %v", err)
	}
}
func TestRejectsResponseTrustAndMetadataTamper(t *testing.T) {
	ca, root, key := fixture(t)
	for name, mutate := range map[string]func(*Installation){"gateway": func(i *Installation) { i.GatewayID = "other" }, "root": func(i *Installation) { i.TrustBundlePEM = append(i.TrustBundlePEM, '\n') }, "serial": func(i *Installation) { i.Serial = "9" }, "time": func(i *Installation) { i.NotAfter++ }} {
		t.Run(name, func(t *testing.T) {
			m, _ := New(Config{Directory: filepath.Join(t.TempDir(), "id"), GatewayID: "gw_1", BootstrapCA: ca})
			p, _ := m.Prepare()
			in := installation(t, p, ca, root, key)
			mutate(&in)
			if m.Install(in) == nil {
				t.Fatal("tampered response accepted")
			}
			again, _ := m.Prepare()
			if !bytes.Equal(p.CSRDER, again.CSRDER) {
				t.Fatal("pending lost after rejection")
			}
		})
	}
}

func TestRejectedReplacementRetainsIncumbent(t *testing.T) {
	ca, root, key := fixture(t)
	m, err := New(Config{Directory: filepath.Join(t.TempDir(), "id"), GatewayID: "gw_1", BootstrapCA: ca})
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Install(installation(t, first, ca, root, key)); err != nil {
		t.Fatal(err)
	}
	incumbent, err := m.GetClientCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := m.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	bad := installation(t, pending, ca, root, key)
	bad.Serial = "substituted"
	if err = m.Install(bad); err == nil {
		t.Fatal("substituted replacement accepted")
	}
	active, err := m.GetClientCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if active.Leaf.SerialNumber.Cmp(incumbent.Leaf.SerialNumber) != 0 {
		t.Fatal("rejected replacement displaced incumbent")
	}
}

func TestRecoveryFallsBackToPreviousGeneration(t *testing.T) {
	ca, root, key := fixture(t)
	dir := filepath.Join(t.TempDir(), "id")
	m, _ := New(Config{Directory: dir, GatewayID: "gw_1", BootstrapCA: ca})
	p, _ := m.Prepare()
	if err := m.Install(installation(t, p, ca, root, key)); err != nil {
		t.Fatal(err)
	}
	p, _ = m.Prepare()
	if err := m.Install(installation(t, p, ca, root, key)); err != nil {
		t.Fatal(err)
	}
	currentBytes, _ := os.ReadFile(filepath.Join(dir, "current"))
	current := string(bytes.TrimSpace(currentBytes))
	if err := os.WriteFile(filepath.Join(dir, current, "chain.pem"), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := New(Config{Directory: dir, GatewayID: "gw_1", BootstrapCA: ca})
	if err := reloaded.Load(); err != nil || !reloaded.Ready() {
		t.Fatalf("previous recovery failed: %v", err)
	}
}
