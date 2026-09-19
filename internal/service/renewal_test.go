package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

type fakeRenewalPersistence struct {
	prepare      *domain.GatewayCertificate
	prepareErr   error
	prepareCall  int
	finalizeCall int
}

func (f *fakeRenewalPersistence) PrepareRenewal(_ context.Context, _ store.PrepareRenewalInput) (*domain.GatewayCertificate, error) {
	f.prepareCall++
	return f.prepare, f.prepareErr
}

func (f *fakeRenewalPersistence) FinalizeRenewal(context.Context, store.FinalizeRenewalInput) (domain.GatewayCertificate, error) {
	f.finalizeCall++
	return domain.GatewayCertificate{}, nil
}

func TestRenewalExactReplaySkipsSigning(t *testing.T) {
	replay := domain.GatewayCertificate{GatewayID: "gw_1", CertificatePEM: "chain", TrustBundlePEM: "root", AuthorityID: "ca_2", SerialNumber: "10", NotBefore: 1, NotAfter: 2}
	persistence := &fakeRenewalPersistence{prepare: &replay}
	service, err := NewRenewalServiceWithDependencies(signerFunc(func(context.Context, pki.SignRequest) (pki.SignedCertificate, error) {
		t.Fatal("replay must not sign")
		return pki.SignedCertificate{}, nil
	}), RenewalDependencies{Store: persistence, Clock: time.Now, IDs: func() string { return "cert_2" }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Renew(context.Background(), RenewalInput{Credential: RenewalCredential{GatewayID: "gw_1", CertificateID: "cert_1", SerialNumber: "9", Fingerprint: []byte("fp")}, CSRDER: fakeCSR(t, "gw_1")})
	if err != nil || result.CertificatePEM != "chain" || persistence.prepareCall != 1 || persistence.finalizeCall != 0 {
		t.Fatalf("result=%+v prepare=%d finalize=%d err=%v", result, persistence.prepareCall, persistence.finalizeCall, err)
	}
}

func TestRenewalRejectsInvalidCSRAndIncumbentWithoutSigning(t *testing.T) {
	for name, persistence := range map[string]*fakeRenewalPersistence{
		"csr":       {},
		"incumbent": {prepareErr: store.ErrRenewalState},
	} {
		t.Run(name, func(t *testing.T) {
			service, err := NewRenewalServiceWithDependencies(signerFunc(func(context.Context, pki.SignRequest) (pki.SignedCertificate, error) {
				t.Fatal("unauthorized renewal must not sign")
				return pki.SignedCertificate{}, nil
			}), RenewalDependencies{Store: persistence, Clock: time.Now, IDs: func() string { return "cert_2" }})
			if err != nil {
				t.Fatal(err)
			}
			csr := fakeCSR(t, "gw_1")
			if name == "csr" {
				csr = []byte("invalid")
			}
			_, err = service.Renew(context.Background(), RenewalInput{Credential: RenewalCredential{GatewayID: "gw_1", CertificateID: "cert_1", SerialNumber: "9", Fingerprint: []byte("fp")}, CSRDER: csr})
			var invalid *InvalidCredentialError
			if !errors.As(err, &invalid) {
				t.Fatalf("error=%T %v", err, err)
			}
		})
	}
}
