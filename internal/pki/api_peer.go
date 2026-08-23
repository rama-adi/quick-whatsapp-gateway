package pki

import (
	"bytes"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"time"
)

// CanonicalCertPool accepts exactly one canonical PEM certificate and no trailing material.
func CanonicalCertPool(caPEM []byte) (*x509.CertPool, *x509.Certificate, error) {
	block, rest := pem.Decode(caPEM)
	canonical := block != nil && block.Type == "CERTIFICATE" &&
		len(bytes.TrimSpace(rest)) == 0 && bytes.Equal(caPEM, pem.EncodeToMemory(block))
	if !canonical {
		return nil, nil, errors.New("pki: bootstrap CA must be one canonical certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA || !cert.BasicConstraintsValid {
		return nil, nil, errors.New("pki: invalid bootstrap CA")
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool, cert, nil
}

// NewAPIPeerTLSConfig performs complete SPIFFE verification without OS roots or hostname fallback.
func NewAPIPeerTLSConfig(caPEM []byte, now func() time.Time) (*tls.Config, error) {
	roots, _, err := CanonicalCertPool(caPEM)
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // #nosec G402 -- full verification below replaces hostname verification.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("pki: API certificate missing")
			}
			leaf := state.PeerCertificates[0]
			intermediates := x509.NewCertPool()
			for _, cert := range state.PeerCertificates[1:] {
				intermediates.AddCert(cert)
			}
			if _, verifyErr := leaf.Verify(x509.VerifyOptions{
				Roots:         roots,
				Intermediates: intermediates,
				CurrentTime:   now().UTC(),
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}); verifyErr != nil {
				return errors.New("pki: API certificate verification failed")
			}
			if !apiIdentityConstraintsMet(leaf) {
				return errors.New("pki: invalid API identity")
			}
			return nil
		},
	}, nil
}

// apiIdentityConstraintsMet reports whether a verified peer certificate carries
// exactly the API identity policy: Ed25519, non-CA, digital-signature only,
// server+client EKU, and the single canonical SPIFFE URI.
func apiIdentityConstraintsMet(leaf *x509.Certificate) bool {
	if _, ok := leaf.PublicKey.(ed25519.PublicKey); !ok || leaf.PublicKeyAlgorithm != x509.Ed25519 {
		return false
	}
	if leaf.IsCA || !leaf.BasicConstraintsValid || leaf.KeyUsage != x509.KeyUsageDigitalSignature {
		return false
	}
	hasServerClientEKU := len(leaf.ExtKeyUsage) == 2 &&
		leaf.ExtKeyUsage[0] == x509.ExtKeyUsageServerAuth && leaf.ExtKeyUsage[1] == x509.ExtKeyUsageClientAuth
	hasSingleAPIURI := len(leaf.URIs) == 1 && leaf.URIs[0].String() == APIIdentityURI
	hasNoNameSANs := len(leaf.DNSNames) == 0 && len(leaf.IPAddresses) == 0 && len(leaf.EmailAddresses) == 0
	return hasServerClientEKU && hasSingleAPIURI && hasNoNameSANs
}
