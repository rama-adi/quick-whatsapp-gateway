package pki

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"io"
	"math/big"
	"net/url"
	"regexp"
	"time"
)

const MaxCSRBytes = 16 << 10

var oidSAN = asn1.ObjectIdentifier{2, 5, 29, 17}

var gatewayIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ValidateGatewayID(id string) error {
	if !gatewayIDPattern.MatchString(id) {
		return errors.New("invalid gateway id")
	}
	return nil
}

type ValidatedCSR struct {
	der       []byte
	gatewayID string
	hash      [32]byte
}

func (v ValidatedCSR) DER() []byte       { return append([]byte(nil), v.der...) }
func (v ValidatedCSR) DERHash() [32]byte { return v.hash }

func ValidateCSR(der []byte, gatewayID string) (ValidatedCSR, error) {
	if err := ValidateGatewayID(gatewayID); err != nil {
		return ValidatedCSR{}, err
	}
	if len(der) == 0 || len(der) > MaxCSRBytes {
		return ValidatedCSR{}, errors.New("invalid CSR size")
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil || csr.CheckSignature() != nil {
		return ValidatedCSR{}, errors.New("invalid CSR")
	}
	if _, ok := csr.PublicKey.(ed25519.PublicKey); !ok || csr.PublicKeyAlgorithm != x509.Ed25519 {
		return ValidatedCSR{}, errors.New("CSR key must be Ed25519")
	}
	if csr.Subject.CommonName != "" && csr.Subject.CommonName != gatewayID {
		return ValidatedCSR{}, errors.New("invalid CSR common name")
	}
	if len(csr.DNSNames) != 0 || len(csr.IPAddresses) != 0 || len(csr.EmailAddresses) != 0 || len(csr.URIs) != 1 {
		return ValidatedCSR{}, errors.New("CSR must contain exactly one URI SAN")
	}
	expected := "spiffe://quick-wa/gateway/" + gatewayID
	if csr.URIs[0].String() != expected {
		return ValidatedCSR{}, errors.New("invalid gateway SPIFFE URI")
	}
	for _, ext := range csr.Extensions {
		if !ext.Id.Equal(oidSAN) {
			return ValidatedCSR{}, errors.New("unsupported CSR extension")
		}
	}
	canonical := append([]byte(nil), csr.Raw...)
	return ValidatedCSR{der: canonical, gatewayID: gatewayID, hash: sha256.Sum256(canonical)}, nil
}

type SignRequest struct {
	GatewayID, EnrollmentTokenID, AuthorityID string
	CSR                                       ValidatedCSR
}
type SignedCertificate struct {
	DER, ChainPEM, TrustBundlePEM, Fingerprint []byte
	AuthorityID                                string
	Serial                                     *big.Int
	NotBefore, NotAfter                        time.Time
}
type Signer interface {
	Sign(context.Context, SignRequest) (SignedCertificate, error)
}

type Policy struct{ TTL, Skew time.Duration }

func NewPolicy(ttl, skew time.Duration) (Policy, error) {
	if ttl <= 0 || ttl > 24*time.Hour || skew < 0 || skew > 15*time.Minute {
		return Policy{}, errors.New("invalid certificate policy")
	}
	return Policy{TTL: ttl, Skew: skew}, nil
}
func NewLeafTemplate(gatewayID string, csr ValidatedCSR, policy Policy, now, issuerNotAfter time.Time, randomness io.Reader) (*x509.Certificate, error) {
	if _, err := NewPolicy(policy.TTL, policy.Skew); err != nil {
		return nil, err
	}
	if csr.gatewayID != gatewayID || len(csr.der) == 0 {
		return nil, errors.New("validated CSR required")
	}
	rebound, err := ValidateCSR(csr.der, gatewayID)
	if err != nil || rebound.hash != csr.hash {
		return nil, errors.New("invalid validated CSR")
	}
	parsed, _ := x509.ParseCertificateRequest(csr.der)
	serial, err := NewSerial(randomness)
	if err != nil {
		return nil, err
	}
	notBefore := now.Add(-policy.Skew)
	notAfter := now.Add(policy.TTL)
	if issuerNotAfter.Before(notAfter) {
		notAfter = issuerNotAfter
	}
	if !notAfter.After(now) {
		return nil, errors.New("issuer validity exhausted")
	}
	return &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: gatewayID}, NotBefore: notBefore, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, URIs: []*url.URL{parsed.URIs[0]}, BasicConstraintsValid: true}, nil
}

func NewSerial(randomness io.Reader) (*big.Int, error) {
	if randomness == nil {
		randomness = rand.Reader
	}
	b := make([]byte, 16)
	if _, err := io.ReadFull(randomness, b); err != nil {
		return nil, err
	}
	serial := new(big.Int).SetBytes(b)
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	return serial, nil
}
