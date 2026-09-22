package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"testing"
	"time"
)

func apiPeerFixture(t *testing.T, mutate func(*x509.Certificate)) ([]byte, tls.ConnectionState) {
	now := time.Now().UTC()
	rootPub, rootKey, _ := ed25519.GenerateKey(rand.Reader)
	rootT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, _ := x509.CreateCertificate(rand.Reader, rootT, rootT, rootPub, rootKey)
	root, _ := x509.ParseCertificate(rootDER)
	leafPub, _, _ := ed25519.GenerateKey(rand.Reader)
	u, _ := url.Parse(APIIdentityURI)
	leafT := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, URIs: []*url.URL{u}}
	if mutate != nil {
		mutate(leafT)
	}
	leafDER, _ := x509.CreateCertificate(rand.Reader, leafT, root, leafPub, rootKey)
	leaf, _ := x509.ParseCertificate(leafDER)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}), tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
}
func TestAPIPeerVerifierStrictMatrix(t *testing.T) {
	ca, state := apiPeerFixture(t, nil)
	cfg, err := NewAPIPeerTLSConfig(ca, time.Now)
	if err != nil || cfg.MinVersion != tls.VersionTLS13 || cfg.RootCAs != nil {
		t.Fatalf("config: %v", err)
	}
	if err = cfg.VerifyConnection(state); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*x509.Certificate){"CA": func(c *x509.Certificate) { c.IsCA = true }, "DNS": func(c *x509.Certificate) { c.DNSNames = []string{"api"} }, "client": func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} }, "extra URI": func(c *x509.Certificate) { c.URIs = append(c.URIs, c.URIs[0]) }, "wrong key usage": func(c *x509.Certificate) { c.KeyUsage |= x509.KeyUsageCertSign }} {
		t.Run(name, func(t *testing.T) {
			ca, state := apiPeerFixture(t, mutate)
			cfg, _ := NewAPIPeerTLSConfig(ca, time.Now)
			if cfg.VerifyConnection(state) == nil {
				t.Fatal("invalid peer accepted")
			}
		})
	}
	if _, err = NewAPIPeerTLSConfig(append(ca, '\n'), time.Now); err == nil {
		t.Fatal("trailing CA accepted")
	}
}
