package main

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

	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki/gatewayidentity"
)

func privateEngineTestConfig(t *testing.T) *tls.Config {
	t.Helper()
	now := time.Now().UTC()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, rootT, rootT, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	pemRoot := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	identity, err := gatewayidentity.New(gatewayidentity.Config{Directory: t.TempDir(), GatewayID: "gw", BootstrapCA: pemRoot})
	if err != nil {
		t.Fatal(err)
	}
	config, err := privateEngineTLSConfig(identity)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func privateEnginePeer(t *testing.T, uri string) tls.ConnectionState {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(uri)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, URIs: []*url.URL{u}, PublicKey: pub}
	return tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
}

func TestPrivateEngineTLSRejectsHostilePeers(t *testing.T) {
	config := privateEngineTestConfig(t)
	if config.MinVersion != tls.VersionTLS13 || config.ClientAuth != tls.RequireAndVerifyClientCert || config.ClientCAs == nil {
		t.Fatalf("private TLS policy = %#v", config)
	}
	if err := config.VerifyConnection(privateEnginePeer(t, pki.APIIdentityURI)); err != nil {
		t.Fatalf("valid API identity rejected: %v", err)
	}
	for _, state := range []tls.ConnectionState{{}, privateEnginePeer(t, "spiffe://quick-wa/gateway/other"), privateEnginePeer(t, "spiffe://other/api")} {
		if config.VerifyConnection(state) == nil {
			t.Fatal("hostile peer accepted")
		}
	}
	// The standard TLS verifier applies ClientCAs before VerifyConnection; an
	// untrusted chain cannot reach an authenticated RPC on this listener.
	if config.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatal("untrusted client chain is not required to verify")
	}
}
