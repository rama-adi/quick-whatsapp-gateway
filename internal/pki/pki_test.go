package pki

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"net/url"
	"testing"
	"time"
)

func csrDER(t *testing.T, gateway string, mutate func(*x509.CertificateRequest)) []byte {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("spiffe://quick-wa/gateway/" + gateway)
	template := &x509.CertificateRequest{Subject: pkix.Name{CommonName: gateway}, URIs: []*url.URL{u}}
	if mutate != nil {
		mutate(template)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
func TestValidateCSRAndLeafPolicy(t *testing.T) {
	validated, err := ValidateCSR(csrDER(t, "gw_1", nil), "gw_1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	policy, _ := NewPolicy(24*time.Hour, 5*time.Minute)
	cert, err := NewLeafTemplate("gw_1", validated, policy, now, now.Add(12*time.Hour), bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.ExtKeyUsage) != 2 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || cert.ExtKeyUsage[1] != x509.ExtKeyUsageServerAuth || cert.NotAfter.After(now.Add(12*time.Hour)) {
		t.Fatal("bad leaf policy")
	}
	if !cert.NotBefore.Equal(now.Add(-5 * time.Minute)) {
		t.Fatal("policy skew not applied")
	}
	if len(cert.ExtraExtensions) != 0 || cert.SerialNumber.Sign() <= 0 || cert.SerialNumber.BitLen() > 128 {
		t.Fatal("unsafe template")
	}
}
func TestValidateCSRRejectsNamesAndExtensions(t *testing.T) {
	tests := []func(*x509.CertificateRequest){
		func(c *x509.CertificateRequest) { c.DNSNames = []string{"example.com"} },
		func(c *x509.CertificateRequest) { c.URIs = append(c.URIs, c.URIs[0]) },
		func(c *x509.CertificateRequest) { c.Subject.CommonName = "other" },
		func(c *x509.CertificateRequest) {
			c.ExtraExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 19}, Value: []byte{0x30, 0}}}
		},
	}
	for i, mutate := range tests {
		if _, err := ValidateCSR(csrDER(t, "gw_1", mutate), "gw_1"); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
func TestGatewayIDAndOpaqueCSR(t *testing.T) {
	for _, id := range []string{"", "a.b", "a/b", "a%2Fb", "a b", string(make([]byte, 65))} {
		if ValidateGatewayID(id) == nil {
			t.Errorf("accepted %q", id)
		}
	}
	if _, e := NewLeafTemplate("gw", ValidatedCSR{}, Policy{TTL: time.Hour}, time.Now(), time.Now().Add(time.Hour), rand.Reader); e == nil {
		t.Fatal("zero CSR accepted")
	}
	der := csrDER(t, "gw", nil)
	v, e := ValidateCSR(der, "gw")
	if e != nil {
		t.Fatal(e)
	}
	der[0] ^= 1
	p, _ := NewPolicy(time.Hour, time.Minute)
	if _, e := NewLeafTemplate("gw", v, p, time.Now(), time.Now().Add(time.Hour), rand.Reader); e != nil {
		t.Fatal("source mutation affected validated CSR")
	}
}
func TestEnvelopeTamperAndSubstitution(t *testing.T) {
	key := make([]byte, 32)
	binding := KeyBinding{AuthorityID: "ca1", Kind: "root", KeyID: "k1", CertificateFingerprint: []byte("fp")}
	env, err := SealKey(key, []byte("private"), binding, bytes.NewReader(make([]byte, 12)))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := OpenKey(key, env, binding)
	if err != nil || string(plain) != "private" {
		t.Fatal("roundtrip")
	}
	tampered := env
	tampered.Ciphertext = append([]byte(nil), env.Ciphertext...)
	tampered.Ciphertext[0] ^= 1
	if _, err := OpenKey(key, tampered, binding); err == nil {
		t.Fatal("tamper accepted")
	}
	binding.AuthorityID = "ca2"
	if _, err := OpenKey(key, env, binding); err == nil {
		t.Fatal("substitution accepted")
	}
}

func TestValidateSignedGateway(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rootPub, rootKey, _ := ed25519.GenerateKey(rand.Reader)
	rootT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, MaxPathLen: 1, KeyUsage: x509.KeyUsageCertSign}
	rootDER, _ := x509.CreateCertificate(rand.Reader, rootT, rootT, rootPub, rootKey)
	root, _ := x509.ParseCertificate(rootDER)
	issuerPub, issuerKey, _ := ed25519.GenerateKey(rand.Reader)
	issuerT := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "issuer"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour), IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign}
	issuerDER, _ := x509.CreateCertificate(rand.Reader, issuerT, root, issuerPub, rootKey)
	issuer, _ := x509.ParseCertificate(issuerDER)
	csr, _ := ValidateCSR(csrDER(t, "gw", nil), "gw")
	leafT, _ := NewLeafTemplate("gw", csr, Policy{TTL: time.Hour, Skew: time.Minute}, now, issuer.NotAfter, rand.Reader)
	parsedCSR, _ := x509.ParseCertificateRequest(csr.DER())
	leafDER, _ := x509.CreateCertificate(rand.Reader, leafT, issuer, parsedCSR.PublicKey, issuerKey)
	fp := sha256.Sum256(leafDER)
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuerDER})...)
	signed := SignedCertificate{DER: leafDER, ChainPEM: chain, TrustBundlePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}), Fingerprint: fp[:], AuthorityID: "issuer", Serial: leafT.SerialNumber, NotBefore: leafT.NotBefore, NotAfter: leafT.NotAfter}
	if err := ValidateSignedGateway(signed, csr, "gw", now); err != nil {
		t.Fatal(err)
	}
	badFingerprint := signed
	badFingerprint.Fingerprint = append([]byte(nil), signed.Fingerprint...)
	badFingerprint.Fingerprint[0] ^= 1
	if ValidateSignedGateway(badFingerprint, csr, "gw", now) == nil {
		t.Fatal("tampered fingerprint accepted")
	}
	for name, mutate := range map[string]func(*x509.Certificate){
		"ca":                 func(c *x509.Certificate) { c.IsCA = true },
		"constraints absent": func(c *x509.Certificate) { c.BasicConstraintsValid = false },
		"cert sign":          func(c *x509.Certificate) { c.KeyUsage |= x509.KeyUsageCertSign },
		"crl sign":           func(c *x509.Certificate) { c.KeyUsage |= x509.KeyUsageCRLSign },
		"key encipherment":   func(c *x509.Certificate) { c.KeyUsage |= x509.KeyUsageKeyEncipherment },
	} {
		t.Run(name, func(t *testing.T) {
			badLeaf := *leafT
			mutate(&badLeaf)
			der, err := x509.CreateCertificate(rand.Reader, &badLeaf, issuer, parsedCSR.PublicKey, issuerKey)
			if err != nil {
				t.Fatal(err)
			}
			parsed, _ := x509.ParseCertificate(der)
			fingerprint := sha256.Sum256(der)
			out := signed
			out.DER, out.Fingerprint, out.Serial, out.NotBefore, out.NotAfter = der, fingerprint[:], parsed.SerialNumber, parsed.NotBefore, parsed.NotAfter
			out.ChainPEM = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuerDER})...)
			if ValidateSignedGateway(out, csr, "gw", now) == nil {
				t.Fatal("malformed leaf accepted")
			}
		})
	}
}

func TestValidateSignedAPIExactIdentityEKUAndChain(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rootPub, rootKey, _ := ed25519.GenerateKey(rand.Reader)
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(31), Subject: pkix.Name{CommonName: "root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, MaxPathLen: 1, KeyUsage: x509.KeyUsageCertSign}
	rootDER, _ := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, rootPub, rootKey)
	root, _ := x509.ParseCertificate(rootDER)
	issuerPub, issuerKey, _ := ed25519.GenerateKey(rand.Reader)
	issuerTemplate := &x509.Certificate{SerialNumber: big.NewInt(32), Subject: pkix.Name{CommonName: "issuer"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour), IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign}
	issuerDER, _ := x509.CreateCertificate(rand.Reader, issuerTemplate, root, issuerPub, rootKey)
	issuer, _ := x509.ParseCertificate(issuerDER)
	publicKey, _, _ := ed25519.GenerateKey(rand.Reader)
	makeSigned := func(mutate func(*x509.Certificate)) SignedCertificate {
		template, _ := NewAPILeafTemplate(publicKey, Policy{TTL: time.Hour, Skew: time.Minute}, now, issuer.NotAfter, rand.Reader)
		if mutate != nil {
			mutate(template)
		}
		der, _ := x509.CreateCertificate(rand.Reader, template, issuer, publicKey, issuerKey)
		leaf, _ := x509.ParseCertificate(der)
		fingerprint := sha256.Sum256(der)
		return SignedCertificate{DER: der, ChainPEM: append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuerDER})...), TrustBundlePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}), Fingerprint: fingerprint[:], AuthorityID: "issuer", Serial: leaf.SerialNumber, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter}
	}
	if err := ValidateSignedAPI(makeSigned(nil), publicKey, now); err != nil {
		t.Fatal(err)
	}
	wrongURI, _ := url.Parse("spiffe://quick-wa/api/other")
	for name, mutate := range map[string]func(*x509.Certificate){
		"identity":   func(c *x509.Certificate) { c.URIs = []*url.URL{wrongURI} },
		"client EKU": func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} },
		"extra EKU":  func(c *x509.Certificate) { c.ExtKeyUsage = append(c.ExtKeyUsage, x509.ExtKeyUsageClientAuth) },
		"CA":         func(c *x509.Certificate) { c.IsCA = true },
	} {
		t.Run(name, func(t *testing.T) {
			if ValidateSignedAPI(makeSigned(mutate), publicKey, now) == nil {
				t.Fatal("invalid API leaf accepted")
			}
		})
	}
	badChain := makeSigned(nil)
	badChain.ChainPEM = badChain.ChainPEM[:len(badChain.ChainPEM)/2]
	if ValidateSignedAPI(badChain, publicKey, now) == nil {
		t.Fatal("truncated API chain accepted")
	}
	_, foreignKey, _ := ed25519.GenerateKey(rand.Reader)
	if ValidateSignedAPI(makeSigned(nil), foreignKey.Public().(ed25519.PublicKey), now) == nil {
		t.Fatal("foreign API key accepted")
	}
}
