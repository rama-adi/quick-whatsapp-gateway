package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/enrollmenttoken"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/pki"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

type stubSigner struct{}

func (stubSigner) Sign(context.Context, pki.SignRequest) (pki.SignedCertificate, error) {
	return pki.SignedCertificate{}, nil
}

type signerFunc func(context.Context, pki.SignRequest) (pki.SignedCertificate, error)

func (f signerFunc) Sign(ctx context.Context, in pki.SignRequest) (pki.SignedCertificate, error) {
	return f(ctx, in)
}

type fakeEnrollmentPersistence struct {
	lookup                                                     store.SelectorRecord
	lookupErr, replaceErr, acquireErr, finalizeErr, recoverErr error
	acquire                                                    store.AcquireEnrollmentLeaseResult
	releases                                                   int
	finalizeCalls                                              int
	finalizeCert                                               domain.GatewayCertificate
}

func (*fakeEnrollmentPersistence) CreatePendingWithToken(context.Context, domain.Gateway, *string, string, domain.EnrollmentToken, []domain.AuditEvent) error {
	return nil
}
func (f *fakeEnrollmentPersistence) LookupSelector(context.Context, string) (store.SelectorRecord, error) {
	return f.lookup, f.lookupErr
}
func (f *fakeEnrollmentPersistence) ReplaceLiveToken(context.Context, string, domain.EnrollmentToken, domain.AuditEvent, int64) error {
	return f.replaceErr
}
func (f *fakeEnrollmentPersistence) AcquireEnrollmentLease(context.Context, store.AcquireEnrollmentLeaseInput) (store.AcquireEnrollmentLeaseResult, error) {
	return f.acquire, f.acquireErr
}
func (f *fakeEnrollmentPersistence) FinalizeEnrollmentIssuance(context.Context, store.FinalizeEnrollmentIssuanceInput) (domain.GatewayCertificate, error) {
	f.finalizeCalls++
	return f.finalizeCert, f.finalizeErr
}
func (f *fakeEnrollmentPersistence) ReleaseEnrollmentLease(context.Context, store.ReleaseEnrollmentLeaseInput) (bool, error) {
	f.releases++
	return true, nil
}
func (f *fakeEnrollmentPersistence) RecoverEnrollmentIssuance(context.Context, store.RecoverEnrollmentIssuanceInput) (domain.GatewayCertificate, error) {
	return domain.GatewayCertificate{}, f.recoverErr
}

func validFakeCredential(t *testing.T) (string, store.SelectorRecord) {
	t.Helper()
	token, err := enrollmenttoken.Generate()
	if err != nil {
		t.Fatal(err)
	}
	digest := enrollmenttoken.Digest(token.String())
	return token.String(), store.SelectorRecord{TokenID: token.Selector(), GatewayID: "gw_test", Status: "active", TokenHash: digest[:]}
}

func fakeService(t *testing.T, persistence EnrollmentPersistence) *EnrollmentService {
	t.Helper()
	deps := EnrollmentDependencies{Clock: time.Now, Entropy: rand.Reader, IDs: func() string { return "01ARZ3NDEKTSV4RRFFQ69G5FAV" }, DenialDelay: func(context.Context, time.Time) {}, Store: persistence}
	s, err := NewEnrollmentServiceWithDependencies(nil, stubSigner{}, DefaultEnrollmentConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func fakeCSR(t *testing.T, gatewayID string) []byte {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("spiffe://quick-wa/gateway/" + gatewayID)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: gatewayID}, URIs: []*url.URL{u}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestEnrollmentConfig(t *testing.T) {
	c := DefaultEnrollmentConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	c.SignTimeout = c.LeaseTTL
	if c.Validate() == nil {
		t.Fatal("accepted unsafe signing timeout")
	}
	c = DefaultEnrollmentConfig()
	c.SafetyMargin = c.LeaseTTL - c.SignTimeout
	if c.Validate() == nil {
		t.Fatal("accepted signing window at lease boundary")
	}
}

func TestReplaceTokenStateConflictIsNonRetryable(t *testing.T) {
	p := &fakeEnrollmentPersistence{replaceErr: store.ErrEnrollmentState}
	s := fakeService(t, p)
	_, err := s.ReplaceToken(context.Background(), "gw_missing", "user_1")
	var conflict *StateConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, store.ErrEnrollmentState) {
		t.Fatalf("error=%T %v", err, err)
	}
	var transient *TransientError
	if errors.As(err, &transient) {
		t.Fatalf("state conflict mapped retryable: %v", err)
	}
}
func TestCredentialDenialUsesTypedErrorAndDelaySeam(t *testing.T) {
	db, _, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = db.Close() }()
	calls := 0
	deps := EnrollmentDependencies{Clock: time.Now, Entropy: rand.Reader, IDs: func() string { return "01ARZ3NDEKTSV4RRFFQ69G5FAV" }, DenialDelay: func(context.Context, time.Time) { calls++ }}
	s, e := NewEnrollmentServiceWithDependencies(db, stubSigner{}, DefaultEnrollmentConfig(), deps)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Redeem(context.Background(), "malformed", nil)
	var invalid *InvalidCredentialError
	if !errors.As(e, &invalid) || calls != 1 {
		t.Fatalf("error=%T delay=%d", e, calls)
	}
}

func TestCredentialDenialPreservesCancellation(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	deps := EnrollmentDependencies{Clock: time.Now, Entropy: rand.Reader, IDs: func() string { return "01ARZ3NDEKTSV4RRFFQ69G5FAV" }, DenialDelay: defaultDenialDelay}
	s, err := NewEnrollmentServiceWithDependencies(db, stubSigner{}, DefaultEnrollmentConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.Redeem(ctx, "malformed", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation became %T", err)
	}
}

func TestAcquireFailuresKeepTypedCauses(t *testing.T) {
	token, lookup := validFakeCredential(t)
	for name, tc := range map[string]struct {
		err         error
		wantContext bool
	}{
		"cancel":   {err: context.Canceled, wantContext: true},
		"database": {err: errors.New("database unavailable")},
	} {
		t.Run(name, func(t *testing.T) {
			p := &fakeEnrollmentPersistence{lookup: lookup, acquireErr: tc.err}
			_, err := fakeService(t, p).Redeem(context.Background(), token, []byte("invalid csr"))
			if tc.wantContext {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%T %v", err, err)
				}
				return
			}
			var transient *TransientError
			if !errors.As(err, &transient) || !errors.Is(err, tc.err) {
				t.Fatalf("error=%T %v", err, err)
			}
		})
	}
}

func TestAcquireDispositionsAreStable(t *testing.T) {
	token, lookup := validFakeCredential(t)
	for disposition, target := range map[string]any{
		"invalid":      (*InvalidCredentialError)(nil),
		"in_progress":  (*InProgressError)(nil),
		"rate_limited": (*RateLimitedError)(nil),
	} {
		t.Run(disposition, func(t *testing.T) {
			p := &fakeEnrollmentPersistence{lookup: lookup, acquire: store.AcquireEnrollmentLeaseResult{Disposition: disposition}}
			_, err := fakeService(t, p).Redeem(context.Background(), token, []byte("invalid csr"))
			switch target.(type) {
			case *InvalidCredentialError:
				var typed *InvalidCredentialError
				if !errors.As(err, &typed) {
					t.Fatalf("error=%T", err)
				}
			case *InProgressError:
				var typed *InProgressError
				if !errors.As(err, &typed) {
					t.Fatalf("error=%T", err)
				}
			case *RateLimitedError:
				var typed *RateLimitedError
				if !errors.As(err, &typed) {
					t.Fatalf("error=%T", err)
				}
			}
		})
	}
}

func TestAcquireDispositionReplayReturnsStoredCertificate(t *testing.T) {
	token, lookup := validFakeCredential(t)
	cert := domain.GatewayCertificate{
		GatewayID:      lookup.GatewayID,
		CertificatePEM: "-----BEGIN CERTIFICATE-----\nREPLAY\n-----END CERTIFICATE-----",
		TrustBundlePEM: "-----BEGIN CERTIFICATE-----\nREPLAY-TRUST\n-----END CERTIFICATE-----",
		AuthorityID:    "authority_replay",
		SerialNumber:   "r-serial",
		NotBefore:      123456789,
		NotAfter:       234567890,
	}
	p := &fakeEnrollmentPersistence{
		lookup: lookup,
		acquire: store.AcquireEnrollmentLeaseResult{
			Disposition: "replay",
			Certificate: &cert,
		},
	}
	s := fakeService(t, p)
	signerCalls := 0
	s.signer = signerFunc(func(context.Context, pki.SignRequest) (pki.SignedCertificate, error) {
		signerCalls++
		return pki.SignedCertificate{}, nil
	})
	got, err := s.Redeem(context.Background(), token, fakeCSR(t, lookup.GatewayID))
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if got.GatewayID != cert.GatewayID || got.CertificatePEM != cert.CertificatePEM || got.TrustBundlePEM != cert.TrustBundlePEM || got.AuthorityID != cert.AuthorityID || got.SerialNumber != cert.SerialNumber || got.NotBefore != cert.NotBefore || got.NotAfter != cert.NotAfter {
		t.Fatalf("unexpected replay result: %#v", got)
	}
	if signerCalls != 0 || p.releases != 0 || p.finalizeCalls != 0 {
		t.Fatalf("expected no signer/release/finalize calls, got signer=%d releases=%d finalize=%d", signerCalls, p.releases, p.finalizeCalls)
	}
}

func TestResolveCredentialAlwaysCallsVerify(t *testing.T) {
	validToken, lookup := validFakeCredential(t)
	mismatched := make([]byte, len(lookup.TokenHash))
	copy(mismatched, lookup.TokenHash)
	mismatched[0] ^= 0xFF
	mismatchedLookup := lookup
	mismatchedLookup.TokenHash = mismatched

	for name, tc := range map[string]struct {
		token   string
		persist *fakeEnrollmentPersistence
	}{
		"malformed token": {
			token: "malformed",
			persist: &fakeEnrollmentPersistence{
				lookup:    store.SelectorRecord{},
				lookupErr: errors.New("lookup skipped"),
			},
		},
		"lookup error": {
			token: validToken,
			persist: &fakeEnrollmentPersistence{
				lookup:    lookup,
				lookupErr: errors.New("lookup failed"),
			},
		},
		"wrong digest": {
			token:   validToken,
			persist: &fakeEnrollmentPersistence{lookup: mismatchedLookup},
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			original := enrollmentTokenVerify
			enrollmentTokenVerify = func(token string, digest [32]byte) bool {
				calls++
				return original(token, digest)
			}
			t.Cleanup(func() { enrollmentTokenVerify = original })

			credential := fakeService(t, tc.persist).resolveCredential(context.Background(), tc.token)
			if credential.credential {
				t.Fatalf("credential unexpectedly valid for %s", name)
			}
			if calls != 1 {
				t.Fatalf("verify calls=%d for %s", calls, name)
			}
		})
	}
}

func TestSignerFailureUsesDetachedRelease(t *testing.T) {
	token, lookup := validFakeCredential(t)
	p := &fakeEnrollmentPersistence{lookup: lookup, acquire: store.AcquireEnrollmentLeaseResult{Disposition: "acquired"}}
	s := fakeService(t, p)
	s.signer = signerFunc(func(context.Context, pki.SignRequest) (pki.SignedCertificate, error) {
		return pki.SignedCertificate{}, errors.New("sign failed")
	})
	_, err := s.Redeem(context.Background(), token, fakeCSR(t, lookup.GatewayID))
	var transient *TransientError
	if !errors.As(err, &transient) || p.releases != 1 {
		t.Fatalf("error=%T releases=%d", err, p.releases)
	}
}

func TestSignerCancellationIsPreservedAndReleased(t *testing.T) {
	token, lookup := validFakeCredential(t)
	p := &fakeEnrollmentPersistence{lookup: lookup, acquire: store.AcquireEnrollmentLeaseResult{Disposition: "acquired"}}
	s := fakeService(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	s.signer = signerFunc(func(context.Context, pki.SignRequest) (pki.SignedCertificate, error) {
		cancel()
		return pki.SignedCertificate{}, context.Canceled
	})
	_, err := s.Redeem(ctx, token, fakeCSR(t, lookup.GatewayID))
	if !errors.Is(err, context.Canceled) || p.releases != 1 {
		t.Fatalf("error=%T releases=%d", err, p.releases)
	}
}

func TestConsumedSigningWindowSkipsSigner(t *testing.T) {
	token, lookup := validFakeCredential(t)
	p := &fakeEnrollmentPersistence{lookup: lookup, acquire: store.AcquireEnrollmentLeaseResult{Disposition: "acquired"}}
	base := time.Unix(100, 0)
	calls := 0
	clock := func() time.Time {
		calls++
		if calls >= 3 {
			return base.Add(time.Hour)
		}
		return base
	}
	deps := EnrollmentDependencies{Clock: clock, Entropy: rand.Reader, IDs: func() string { return "01ARZ3NDEKTSV4RRFFQ69G5FAV" }, DenialDelay: func(context.Context, time.Time) {}, Store: p}
	s, err := NewEnrollmentServiceWithDependencies(nil, stubSigner{}, DefaultEnrollmentConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}
	signerCalls := 0
	s.signer = signerFunc(func(context.Context, pki.SignRequest) (pki.SignedCertificate, error) {
		signerCalls++
		return pki.SignedCertificate{}, nil
	})
	_, err = s.Redeem(context.Background(), token, fakeCSR(t, lookup.GatewayID))
	var transient *TransientError
	if !errors.As(err, &transient) || signerCalls != 0 || p.releases != 1 {
		t.Fatalf("error=%T signer=%d releases=%d", err, signerCalls, p.releases)
	}
}
