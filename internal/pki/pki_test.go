package pki

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
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
