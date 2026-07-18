package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/oklog/ulid/v2"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/enrollmenttoken"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki/localmysql"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

type blockingCertificateSigner struct {
	inner   pki.CertificateSigner
	entered chan struct{}
	resume  chan struct{}
}

type failingCertificateSigner struct{ err error }

func (s failingCertificateSigner) Sign(context.Context, pki.SignRequest) (pki.SignedCertificate, error) {
	return pki.SignedCertificate{}, s.err
}

type ambiguousCommitStore struct {
	inner                   *store.EnrollmentStore
	calls                   atomic.Uint32
	finalizeBefore, recover error
	onFinalize              func()
}

func (s *ambiguousCommitStore) CreatePendingWithToken(ctx context.Context, gateway domain.Gateway, notes *string, creator string, token domain.EnrollmentToken, audits []domain.AuditEvent) error {
	return s.inner.CreatePendingWithToken(ctx, gateway, notes, creator, token, audits)
}

func (s *ambiguousCommitStore) LookupSelector(ctx context.Context, selector string) (store.SelectorRecord, error) {
	return s.inner.LookupSelector(ctx, selector)
}

func (s *ambiguousCommitStore) ReplaceLiveToken(ctx context.Context, gatewayID string, token domain.EnrollmentToken, audit domain.AuditEvent, now int64) error {
	return s.inner.ReplaceLiveToken(ctx, gatewayID, token, audit, now)
}
func (s *ambiguousCommitStore) AcquireEnrollmentLease(ctx context.Context, in store.AcquireEnrollmentLeaseInput) (store.AcquireEnrollmentLeaseResult, error) {
	return s.inner.AcquireEnrollmentLease(ctx, in)
}
func (s *ambiguousCommitStore) FinalizeEnrollmentIssuance(ctx context.Context, in store.FinalizeEnrollmentIssuanceInput) (domain.GatewayCertificate, error) {
	if s.onFinalize != nil {
		s.onFinalize()
	}
	if s.finalizeBefore != nil {
		return domain.GatewayCertificate{}, s.finalizeBefore
	}
	cert, err := s.inner.FinalizeEnrollmentIssuance(ctx, in)
	if err == nil && s.calls.Add(1) == 1 {
		return domain.GatewayCertificate{}, errors.New("injected ambiguous commit result")
	}
	return cert, err
}
func (s *ambiguousCommitStore) ReleaseEnrollmentLease(ctx context.Context, in store.ReleaseEnrollmentLeaseInput) (bool, error) {
	return s.inner.ReleaseEnrollmentLease(ctx, in)
}
func (s *ambiguousCommitStore) RecoverEnrollmentIssuance(ctx context.Context, in store.RecoverEnrollmentIssuanceInput) (domain.GatewayCertificate, error) {
	if s.recover != nil {
		return domain.GatewayCertificate{}, s.recover
	}
	return s.inner.RecoverEnrollmentIssuance(ctx, in)
}

func (b *blockingCertificateSigner) Sign(ctx context.Context, r pki.SignRequest) (pki.SignedCertificate, error) {
	close(b.entered)
	select {
	case <-ctx.Done():
		return pki.SignedCertificate{}, ctx.Err()
	case <-b.resume:
	}
	return b.inner.Sign(ctx, r)
}

func TestEnrollmentMySQLLifecycle(t *testing.T) {
	dsn := os.Getenv("ENROLLMENT_MYSQL_TEST_DSN")
	if dsn == "" || os.Getenv("ENROLLMENT_MYSQL_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit disposable MySQL required")
	}
	db, e := sql.Open("mysql", dsn)
	if e != nil {
		t.Fatalf("redeem: %v (cause: %v)", e, errors.Unwrap(e))
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	var schema string
	_ = db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&schema)
	if !strings.HasPrefix(schema, "qwg_enrollment_test") {
		t.Fatalf("unsafe schema %q", schema)
	}
	for _, q := range []string{"DELETE FROM audit_events", "DELETE FROM gateway_certificates", "DELETE FROM gateway_enrollment_tokens", "DELETE FROM gateways", "DELETE FROM pki_authorities WHERE kind='intermediate'", "DELETE FROM pki_authorities WHERE kind='root'"} {
		if _, e = db.ExecContext(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	policy, _ := pki.NewPolicy(time.Hour, time.Minute)
	ca, e := localmysql.New(db, localmysql.Config{KEK: make([]byte, 32), KeyID: "test", RootTTL: 24 * time.Hour, IntermediateTTL: 4 * time.Hour, RenewBefore: 90 * time.Minute, Policy: policy})
	if e != nil {
		t.Fatal(e)
	}
	if e = ca.EnsureHierarchy(ctx); e != nil {
		t.Fatal(e)
	}
	svc, e := NewEnrollmentService(db, ca, DefaultEnrollmentConfig())
	if e != nil {
		t.Fatal(e)
	}
	issued, e := svc.CreateGateway(ctx, CreateGatewayInput{CreatedByUserID: "user_test"})
	if e != nil {
		t.Fatal(e)
	}
	var storedHash []byte
	var storedPrefix string
	if e = db.QueryRowContext(ctx, "SELECT token_hash,token_prefix FROM gateway_enrollment_tokens WHERE id=?", issued.TokenID).Scan(&storedHash, &storedPrefix); e != nil || len(storedHash) != 32 || storedPrefix == issued.Token || bytes.Contains(storedHash, []byte(issued.Token)) {
		t.Fatal("plaintext enrollment bearer persisted")
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	u, _ := url.Parse("spiffe://quick-wa/gateway/" + issued.GatewayID)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: issued.GatewayID}, URIs: []*url.URL{u}}, key)
	first, e := svc.Redeem(ctx, issued.Token, csr)
	if e != nil {
		t.Fatalf("redeem: %v (cause: %v)", e, errors.Unwrap(e))
	}
	replay, e := svc.Redeem(ctx, issued.Token, csr)
	if e != nil || replay.CertificatePEM != first.CertificatePEM || replay.TrustBundlePEM != first.TrustBundlePEM {
		t.Fatal("exact replay failed")
	}
	// Exact and ambiguous recovery remain valid through normal lifecycle advancement.
	for _, advanced := range []string{"active", "draining", "drained"} {
		if _, e = db.ExecContext(ctx, "UPDATE gateways SET status=? WHERE id=?", advanced, issued.GatewayID); e != nil {
			t.Fatal(e)
		}
		advancedReplay, replayErr := svc.Redeem(ctx, issued.Token, csr)
		if replayErr != nil || advancedReplay.CertificatePEM != first.CertificatePEM {
			t.Fatalf("%s replay: %v", advanced, replayErr)
		}
		digest := enrollmenttoken.Digest(issued.Token)
		csrHash := sha256.Sum256(csr)
		recovered, recoverErr := store.NewEnrollmentStore(db).RecoverEnrollmentIssuance(ctx, store.RecoverEnrollmentIssuanceInput{GatewayID: issued.GatewayID, TokenID: issued.TokenID, ExpectedHash: digest[:], CSRHash: csrHash[:], Now: time.Now().UnixMilli()})
		if recoverErr != nil || recovered.CertificatePEM != first.CertificatePEM {
			t.Fatalf("%s recovery: %v", advanced, recoverErr)
		}
	}
	if _, e = db.ExecContext(ctx, "UPDATE gateways SET status='disabled' WHERE id=?", issued.GatewayID); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Redeem(ctx, issued.Token, csr); !errors.Is(e, ErrEnrollmentDenied) {
		t.Fatalf("disabled gateway replay: %v", e)
	}
	if _, e = db.ExecContext(ctx, "UPDATE gateways SET deleted_at=?,status='disabled' WHERE id=?", time.Now().UnixMilli(), issued.GatewayID); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Redeem(ctx, issued.Token, csr); !errors.Is(e, ErrEnrollmentDenied) {
		t.Fatalf("deleted gateway replay: %v", e)
	}
	if _, e = db.ExecContext(ctx, "UPDATE gateways SET deleted_at=NULL,status='active' WHERE id=?", issued.GatewayID); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ExecContext(ctx, "UPDATE gateway_certificates SET not_after=? WHERE enrollment_token_id=?", time.Now().Add(-time.Minute).UnixMilli(), issued.TokenID); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Redeem(ctx, issued.Token, csr); !errors.Is(e, ErrEnrollmentDenied) {
		t.Fatalf("inactive certificate replay: %v", e)
	}
	if _, e = db.ExecContext(ctx, "UPDATE gateway_certificates SET not_after=? WHERE enrollment_token_id=?", first.NotAfter, issued.TokenID); e != nil {
		t.Fatal(e)
	}
	var status string
	var enrolled sql.NullInt64
	_ = db.QueryRowContext(ctx, "SELECT status,enrolled_at FROM gateways WHERE id=?", issued.GatewayID).Scan(&status, &enrolled)
	if status != "active" || !enrolled.Valid {
		t.Fatal("gateway transition missing")
	}
	if _, e = svc.ReplaceToken(ctx, issued.GatewayID, "user_test"); e == nil {
		t.Fatal("replacement accepted for enrolled gateway")
	} else {
		var conflict *StateConflictError
		if !errors.As(e, &conflict) || !errors.Is(e, store.ErrEnrollmentState) {
			t.Fatalf("enrolled replacement error=%T %v", e, e)
		}
	}
	makeCSR := func(gateway string) []byte {
		_, k, _ := ed25519.GenerateKey(rand.Reader)
		u, _ := url.Parse("spiffe://quick-wa/gateway/" + gateway)
		der, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: gateway}, URIs: []*url.URL{u}}, k)
		return der
	}
	// Concurrent redemption is serialized; a late contender either receives the exact replay or a generic denial.
	concurrent, _ := svc.CreateGateway(ctx, CreateGatewayInput{CreatedByUserID: "user_test"})
	concurrentCSR := makeCSR(concurrent.GatewayID)
	var wg sync.WaitGroup
	results := make(chan EnrollmentResult, 2)
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			r, e := svc.Redeem(ctx, concurrent.Token, concurrentCSR)
			if e == nil {
				results <- r
			}
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for r := range results {
		success++
		if r.TrustBundlePEM == "" {
			t.Fatal("empty concurrent result")
		}
	}
	if success == 0 {
		t.Fatal("no concurrent redeemer succeeded")
	}
	// A successful final commit whose result is lost is recovered as the exact persisted issuance.
	ambiguous, _ := svc.CreateGateway(ctx, CreateGatewayInput{CreatedByUserID: "user_test"})
	ambiguousCSR := makeCSR(ambiguous.GatewayID)
	inner := store.NewEnrollmentStore(db)
	ambiguousSvc, e := NewEnrollmentServiceWithDependencies(db, ca, DefaultEnrollmentConfig(), EnrollmentDependencies{
		Clock: time.Now, Entropy: rand.Reader, IDs: func() string { return ulid.Make().String() },
		DenialDelay: func(context.Context, time.Time) {}, Store: &ambiguousCommitStore{inner: inner},
	})
	if e != nil {
		t.Fatal(e)
	}
	ambiguousResult, e := ambiguousSvc.Redeem(ctx, ambiguous.Token, ambiguousCSR)
	if e != nil || ambiguousResult.CertificatePEM == "" || ambiguousResult.TrustBundlePEM == "" {
		t.Fatalf("ambiguous committed issuance was not recovered: %v", e)
	}
	replayedAmbiguous, e := svc.Redeem(ctx, ambiguous.Token, ambiguousCSR)
	if e != nil || replayedAmbiguous.CertificatePEM != ambiguousResult.CertificatePEM || replayedAmbiguous.TrustBundlePEM != ambiguousResult.TrustBundlePEM {
		t.Fatal("ambiguous commit recovery did not return persisted issuance")
	}
	// A genuine phase-3 storage failure is transient, then detached cleanup releases the owned lease.
	phase3, _ := svc.CreateGateway(ctx, CreateGatewayInput{CreatedByUserID: "user_test"})
	phase3Cause := errors.New("phase3 database failure")
	phase3Store := &ambiguousCommitStore{inner: inner, finalizeBefore: phase3Cause, recover: errors.New("not committed")}
	phase3Svc, e := NewEnrollmentServiceWithDependencies(db, ca, DefaultEnrollmentConfig(), EnrollmentDependencies{Clock: time.Now, Entropy: rand.Reader, IDs: func() string { return ulid.Make().String() }, DenialDelay: func(context.Context, time.Time) {}, Store: phase3Store})
	if e != nil {
		t.Fatal(e)
	}
	_, e = phase3Svc.Redeem(ctx, phase3.Token, makeCSR(phase3.GatewayID))
	var phase3Transient *TransientError
	if !errors.As(e, &phase3Transient) || !errors.Is(e, phase3Cause) {
		t.Fatalf("phase3 error=%T %v", e, e)
	}
	var phase3Status string
	_ = db.QueryRowContext(ctx, "SELECT status FROM gateway_enrollment_tokens WHERE id=?", phase3.TokenID).Scan(&phase3Status)
	if phase3Status != "active" {
		t.Fatalf("phase3 lease status=%s", phase3Status)
	}
	phase3Canceled, _ := svc.CreateGateway(ctx, CreateGatewayInput{CreatedByUserID: "user_test"})
	cancelCtx, cancelPhase3 := context.WithCancel(ctx)
	cancelStore := &ambiguousCommitStore{inner: inner, finalizeBefore: context.Canceled, recover: errors.New("not committed"), onFinalize: cancelPhase3}
	cancelSvc, _ := NewEnrollmentServiceWithDependencies(db, ca, DefaultEnrollmentConfig(), EnrollmentDependencies{Clock: time.Now, Entropy: rand.Reader, IDs: func() string { return ulid.Make().String() }, DenialDelay: func(context.Context, time.Time) {}, Store: cancelStore})
	_, e = cancelSvc.Redeem(cancelCtx, phase3Canceled.Token, makeCSR(phase3Canceled.GatewayID))
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("phase3 cancellation=%T %v", e, e)
	}
	// A signer failure releases only its still-live lease and records the bounded failure audit.
	failed, _ := svc.CreateGateway(ctx, CreateGatewayInput{CreatedByUserID: "user_test"})
	failedSvc, e := NewEnrollmentService(db, failingCertificateSigner{err: errors.New("sign unavailable")}, DefaultEnrollmentConfig())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = failedSvc.Redeem(ctx, failed.Token, makeCSR(failed.GatewayID)); e == nil {
		t.Fatal("signer failure unexpectedly succeeded")
	} else {
		var transient *TransientError
		if !errors.As(e, &transient) {
			t.Fatalf("signer failure error=%T", e)
		}
	}
	var failedStatus string
	var failedNonce, failedCSR []byte
	if e = db.QueryRowContext(ctx, "SELECT status,redemption_nonce,csr_sha256 FROM gateway_enrollment_tokens WHERE id=?", failed.TokenID).Scan(&failedStatus, &failedNonce, &failedCSR); e != nil {
		t.Fatal(e)
	}
	if failedStatus != "active" || failedNonce != nil || failedCSR != nil {
		t.Fatal("signer failure did not release its lease")
	}
	// Invalid audit attribution rejects and rolls back the whole lease acquisition.
	auditReject, _ := svc.CreateGateway(ctx, CreateGatewayInput{CreatedByUserID: "user_test"})
	auditDigest := enrollmenttoken.Digest(auditReject.Token)
	auditCSR := makeCSR(auditReject.GatewayID)
	auditHash := sha256.Sum256(auditCSR)
	_, e = inner.AcquireEnrollmentLease(ctx, store.AcquireEnrollmentLeaseInput{GatewayID: auditReject.GatewayID, TokenID: auditReject.TokenID, ExpectedHash: auditDigest[:], CSRHash: auditHash[:], Nonce: make([]byte, 16), Now: time.Now().UnixMilli(), LeaseUntil: time.Now().Add(time.Minute).UnixMilli(), CSRValid: true, StartedAudit: domain.AuditEvent{ID: ulid.Make().String(), ActorType: "attacker", Action: "enrollment.started", ResourceType: "gateway", Outcome: "success", CreatedAt: time.Now().UnixMilli()}})
	if e == nil {
		t.Fatal("invalid audit actor accepted")
	}
	var auditRejectStatus string
	_ = db.QueryRowContext(ctx, "SELECT status FROM gateway_enrollment_tokens WHERE id=?", auditReject.TokenID).Scan(&auditRejectStatus)
	if auditRejectStatus != "active" {
		t.Fatalf("invalid audit did not roll back lease: %s", auditRejectStatus)
	}
	// Simulate a crashed phase-2 worker: only the identical CSR can reclaim an expired lease.
	crashed, _ := svc.CreateGateway(ctx, CreateGatewayInput{CreatedByUserID: "user_test"})
	crashCSR := makeCSR(crashed.GatewayID)
	h := sha256.Sum256(crashCSR)
	_, e = db.ExecContext(ctx, "UPDATE gateway_enrollment_tokens SET status='redeeming',redemption_nonce=?,csr_sha256=?,redeeming_at=?,lease_expires_at=?,attempt_count=1 WHERE id=?", make([]byte, 16), h[:], time.Now().Add(-2*time.Minute).UnixMilli(), time.Now().Add(-time.Minute).UnixMilli(), crashed.TokenID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Redeem(ctx, crashed.Token, crashCSR); e != nil {
		t.Fatalf("expired identical CSR reclaim: %v", e)
	}
	// An expired lease is reclaimable only by the canonical identical CSR; stale release attempts add no audit.
	different, _ := svc.CreateGateway(ctx, CreateGatewayInput{CreatedByUserID: "user_test"})
	originalCSR := makeCSR(different.GatewayID)
	originalHash := sha256.Sum256(originalCSR)
	staleNonce := make([]byte, 16)
	_, e = db.ExecContext(ctx, "UPDATE gateway_enrollment_tokens SET status='redeeming',redemption_nonce=?,csr_sha256=?,redeeming_at=?,lease_expires_at=?,attempt_count=1 WHERE id=?", staleNonce, originalHash[:], time.Now().Add(-2*time.Minute).UnixMilli(), time.Now().Add(-time.Minute).UnixMilli(), different.TokenID)
	if e != nil {
		t.Fatal(e)
	}
	var failuresBefore int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE action='enrollment.failed' AND resource_id=?", different.GatewayID).Scan(&failuresBefore)
	if _, e = svc.Redeem(ctx, different.Token, makeCSR(different.GatewayID)); !errors.Is(e, ErrEnrollmentDenied) {
		t.Fatalf("different CSR reclaimed expired lease: %v", e)
	}
	var failuresAfter int
	var preservedStatus string
	_ = db.QueryRowContext(ctx, "SELECT status FROM gateway_enrollment_tokens WHERE id=?", different.TokenID).Scan(&preservedStatus)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE action='enrollment.failed' AND resource_id=?", different.GatewayID).Scan(&failuresAfter)
	if preservedStatus != "redeeming" || failuresAfter != failuresBefore {
		t.Fatal("stale denial mutated lease or emitted failure audit")
	}
	// Replacement revokes the old bearer atomically.
	replace, _ := svc.CreateGateway(ctx, CreateGatewayInput{CreatedByUserID: "user_test"})
	next, e := svc.ReplaceToken(ctx, replace.GatewayID, "user_test")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Redeem(ctx, replace.Token, makeCSR(replace.GatewayID)); !errors.Is(e, ErrEnrollmentDenied) {
		t.Fatal("replaced token remained live")
	}
	if _, e = svc.Redeem(ctx, next.Token, makeCSR(replace.GatewayID)); e != nil {
		t.Fatal(e)
	}
	// Replace racing phase 2 cannot deadlock finalize; replacement fences the stale worker.
	race, _ := svc.CreateGateway(ctx, CreateGatewayInput{CreatedByUserID: "user_test"})
	block := &blockingCertificateSigner{inner: ca, entered: make(chan struct{}), resume: make(chan struct{})}
	raceSvc, _ := NewEnrollmentService(db, block, DefaultEnrollmentConfig())
	done := make(chan error, 1)
	go func() { _, e := raceSvc.Redeem(ctx, race.Token, makeCSR(race.GatewayID)); done <- e }()
	<-block.entered
	replaceDone := make(chan error, 1)
	go func() { _, e := svc.ReplaceToken(ctx, race.GatewayID, "user_test"); replaceDone <- e }()
	select {
	case e := <-replaceDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replace/finalize lock-order deadlock")
	}
	close(block.resume)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stale finalizer did not terminate")
	}
	limited, _ := svc.CreateGateway(ctx, CreateGatewayInput{CreatedByUserID: "user_test"})
	_, _ = db.ExecContext(ctx, "UPDATE gateway_enrollment_tokens SET attempt_count=max_attempts WHERE id=?", limited.TokenID)
	_, e = svc.Redeem(ctx, limited.Token, makeCSR(limited.GatewayID))
	var rate *RateLimitedError
	if !errors.As(e, &rate) {
		t.Fatalf("max attempts error=%T", e)
	}
	var limitedStatus string
	_ = db.QueryRowContext(ctx, "SELECT status FROM gateway_enrollment_tokens WHERE id=?", limited.TokenID).Scan(&limitedStatus)
	if limitedStatus != "locked" {
		t.Fatal("max attempts did not lock token")
	}
	// Replay is historical issuance data and does not depend on the currently active issuer.
	if _, e = db.ExecContext(ctx, "UPDATE pki_authorities SET status='retiring' WHERE kind='intermediate' AND status='active'"); e != nil {
		t.Fatal(e)
	}
	historical, e := svc.Redeem(ctx, issued.Token, csr)
	if e != nil || historical.TrustBundlePEM != first.TrustBundlePEM || historical.CertificatePEM != first.CertificatePEM {
		t.Fatal("historical trust replay changed after authority rotation state")
	}
	var userAudits, gatewayAudits int
	if e = db.QueryRowContext(ctx, "SELECT SUM(actor_type='user' AND actor_id='user_test'),SUM(actor_type='gateway' AND actor_id IS NOT NULL) FROM audit_events").Scan(&userAudits, &gatewayAudits); e != nil || userAudits == 0 || gatewayAudits == 0 {
		t.Fatal("authorized actors were not preserved in audit")
	}
	rows, e := db.QueryContext(ctx, "SELECT COALESCE(CAST(metadata AS CHAR),'') FROM audit_events")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var metadata string
		if e = rows.Scan(&metadata); e != nil {
			t.Fatal(e)
		}
		lower := strings.ToLower(metadata)
		for _, secret := range []string{"qwg_enroll_v1_", "certificate_pem", "trust_bundle_pem", "csr_der", "token_hash"} {
			if strings.Contains(lower, secret) {
				t.Fatalf("audit leaked %s", secret)
			}
		}
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
}
