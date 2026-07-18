// Package localmysql implements the development CA backed by the API-owned MySQL database.
// It is deliberately not wired into either binary yet.
package localmysql

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	base "github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
)

var ErrRootRotationRequired = errors.New("pki: root rotation required")

type Config struct {
	KEK                                   []byte
	KeyID                                 string
	RootTTL, IntermediateTTL, RenewBefore time.Duration
	Policy                                base.Policy
}
type cached struct {
	root, intermediate         domain.PKIAuthority
	rootCert, intermediateCert *x509.Certificate
}
type AuthorityRepository interface {
	LoadActive(context.Context, string) (domain.PKIAuthority, error)
	Insert(context.Context, domain.PKIAuthority) error
	RetireActiveIntermediate(context.Context, int64) error
}
type AuthorityTransactions interface {
	WithHierarchyLock(context.Context, func(AuthorityRepository) error) error
}
type Signer struct {
	repo   AuthorityTransactions
	cfg    Config
	now    func() time.Time
	random io.Reader
	mu     sync.RWMutex
	cache  *cached
}

func New(db *sql.DB, cfg Config) (*Signer, error) {
	if db == nil || len(cfg.KEK) != 32 || cfg.KeyID == "" || cfg.RootTTL <= 0 || cfg.IntermediateTTL <= 0 || cfg.IntermediateTTL >= cfg.RootTTL || cfg.RenewBefore <= 0 || cfg.RenewBefore >= cfg.IntermediateTTL {
		return nil, errors.New("pki: invalid local CA config")
	}
	if _, err := base.NewPolicy(cfg.Policy.TTL, cfg.Policy.Skew); err != nil {
		return nil, err
	}
	minimum := cfg.Policy.TTL + cfg.Policy.Skew
	if cfg.IntermediateTTL <= minimum || cfg.RenewBefore < minimum {
		return nil, errors.New("pki: authority policy cannot safely issue leaf lifetime")
	}
	return &Signer{repo: mysqlTransactions{db: db}, cfg: cfg, now: time.Now, random: rand.Reader}, nil
}

func (s *Signer) EnsureHierarchy(ctx context.Context) error {
	now := s.now().UTC()
	var root, intermediate domain.PKIAuthority
	var prepared *cached
	err := s.repo.WithHierarchyLock(ctx, func(repo AuthorityRepository) error {
		var err error
		var rootErr error
		root, rootErr = repo.LoadActive(ctx, "root")
		if rootErr != nil && !errors.Is(rootErr, sql.ErrNoRows) {
			return rootErr
		}
		if errors.Is(rootErr, sql.ErrNoRows) {
			root, err = s.createRoot(now)
			if err != nil {
				return err
			}
			intermediate, err = s.createIntermediate(now, root)
			if err != nil {
				return err
			}
			if err = repo.Insert(ctx, root); err != nil {
				return err
			}
			if err = repo.Insert(ctx, intermediate); err != nil {
				return err
			}
			prepared, err = s.prepare(root, intermediate, now)
			if err != nil {
				return err
			}
			return nil
		}
		rootCert, _, err := s.validate(root, now)
		if err != nil {
			return fmt.Errorf("pki: invalid root: %w", err)
		}
		if !now.Before(rootCert.NotAfter) || now.Before(rootCert.NotBefore) {
			return ErrRootRotationRequired
		}
		if rootCert.NotAfter.Before(now.Add(s.cfg.Policy.TTL + s.cfg.Policy.Skew)) {
			return ErrRootRotationRequired
		}
		var intErr error
		intermediate, intErr = repo.LoadActive(ctx, "intermediate")
		rotate := errors.Is(intErr, sql.ErrNoRows)
		if intErr != nil && !rotate {
			return intErr
		}
		if !rotate {
			ic, _, e := s.validate(intermediate, now)
			if e != nil {
				return fmt.Errorf("pki: invalid intermediate: %w", e)
			}
			if e = ic.CheckSignatureFrom(rootCert); e != nil {
				return fmt.Errorf("pki: invalid intermediate chain: %w", e)
			}
			if now.Before(ic.NotBefore) {
				return fmt.Errorf("pki: intermediate is not yet valid")
			}
			rotate = !now.Add(s.cfg.RenewBefore).Before(ic.NotAfter)
		}
		if rotate {
			intermediate, err = s.createIntermediate(now, root)
			if err != nil {
				return err
			}
			prepared, err = s.prepare(root, intermediate, now)
			if err != nil {
				return err
			}
			// Retire only after the complete successor is generated and validated.
			if intErr == nil {
				if err = repo.RetireActiveIntermediate(ctx, millis(now)); err != nil {
					return err
				}
			}
			if err = repo.Insert(ctx, intermediate); err != nil {
				return err
			}
		} else {
			prepared, err = s.prepare(root, intermediate, now)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cache = prepared
	s.mu.Unlock()
	return nil
}

func (s *Signer) createRoot(now time.Time) (domain.PKIAuthority, error) {
	pub, key, err := ed25519.GenerateKey(s.random)
	if err != nil {
		return domain.PKIAuthority{}, err
	}
	identifier, err := ulid.New(ulid.Timestamp(now), s.random)
	if err != nil {
		return domain.PKIAuthority{}, err
	}
	id := identifier.String()
	serial, err := base.NewSerial(s.random)
	if err != nil {
		return domain.PKIAuthority{}, err
	}
	t := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Quick WA Local Root"}, NotBefore: now.Add(-s.cfg.Policy.Skew), NotAfter: now.Add(s.cfg.RootTTL), IsCA: true, BasicConstraintsValid: true, MaxPathLen: 1, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(s.random, t, t, pub, key)
	if err != nil {
		return domain.PKIAuthority{}, err
	}
	return s.row(id, "root", nil, der, key, now)
}
func (s *Signer) createIntermediate(now time.Time, root domain.PKIAuthority) (domain.PKIAuthority, error) {
	rc, rk, err := s.validate(root, now)
	if err != nil {
		return domain.PKIAuthority{}, err
	}
	pub, key, err := ed25519.GenerateKey(s.random)
	if err != nil {
		return domain.PKIAuthority{}, err
	}
	serial, err := base.NewSerial(s.random)
	if err != nil {
		return domain.PKIAuthority{}, err
	}
	notAfter := now.Add(s.cfg.IntermediateTTL)
	if rc.NotAfter.Before(notAfter) {
		notAfter = rc.NotAfter
	}
	t := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Quick WA Local Intermediate"}, NotBefore: now.Add(-s.cfg.Policy.Skew), NotAfter: notAfter, IsCA: true, BasicConstraintsValid: true, MaxPathLen: 0, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(s.random, t, rc, pub, rk)
	if err != nil {
		return domain.PKIAuthority{}, err
	}
	identifier, err := ulid.New(ulid.Timestamp(now), s.random)
	if err != nil {
		return domain.PKIAuthority{}, err
	}
	id := identifier.String()
	return s.row(id, "intermediate", &root.ID, der, key, now)
}
func (s *Signer) row(id, kind string, parent *string, der []byte, key ed25519.PrivateKey, now time.Time) (domain.PKIAuthority, error) {
	fp := sha256.Sum256(der)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return domain.PKIAuthority{}, err
	}
	env, err := base.SealKey(s.cfg.KEK, pkcs8, base.KeyBinding{AuthorityID: id, Kind: kind, KeyID: s.cfg.KeyID, CertificateFingerprint: fp[:]}, s.random)
	if err != nil {
		return domain.PKIAuthority{}, err
	}
	cert, _ := x509.ParseCertificate(der)
	return domain.PKIAuthority{ID: id, Kind: kind, ParentAuthorityID: parent, Status: "active", CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), CertificateFingerprint: fp[:], EncryptedPrivateKey: env.Ciphertext, PrivateKeyNonce: env.Nonce, EncryptionKeyID: s.cfg.KeyID, NotBefore: millis(cert.NotBefore), NotAfter: millis(cert.NotAfter), CreatedAt: millis(now), UpdatedAt: millis(now)}, nil
}
func (s *Signer) validate(a domain.PKIAuthority, now time.Time) (*x509.Certificate, ed25519.PrivateKey, error) {
	if a.Status != "active" || (a.Kind != "root" && a.Kind != "intermediate") {
		return nil, nil, errors.New("invalid row identity")
	}
	if a.EncryptionKeyID != s.cfg.KeyID {
		return nil, nil, errors.New("encryption key id mismatch")
	}
	b, rest := pem.Decode([]byte(a.CertificatePEM))
	if b == nil || len(rest) != 0 || b.Type != "CERTIFICATE" {
		return nil, nil, errors.New("invalid PEM")
	}
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil, nil, err
	}
	fp := sha256.Sum256(cert.Raw)
	if !bytes.Equal(fp[:], a.CertificateFingerprint) {
		return nil, nil, errors.New("fingerprint mismatch")
	}
	if millis(cert.NotBefore) != a.NotBefore || millis(cert.NotAfter) != a.NotAfter {
		return nil, nil, errors.New("validity mismatch")
	}
	if !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, nil, errors.New("invalid CA constraints")
	}
	if a.Kind == "root" && (a.ParentAuthorityID != nil || cert.CheckSignatureFrom(cert) != nil) {
		return nil, nil, errors.New("invalid root")
	}
	if a.Kind == "intermediate" && (a.ParentAuthorityID == nil || !cert.MaxPathLenZero) {
		return nil, nil, errors.New("invalid intermediate")
	}
	plain, err := base.OpenKey(s.cfg.KEK, base.Envelope{Ciphertext: a.EncryptedPrivateKey, Nonce: a.PrivateKeyNonce}, base.KeyBinding{AuthorityID: a.ID, Kind: a.Kind, KeyID: a.EncryptionKeyID, CertificateFingerprint: a.CertificateFingerprint})
	if err != nil {
		return nil, nil, err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(plain)
	if err != nil {
		return nil, nil, err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	certPublic, certOK := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !certOK || !bytes.Equal(key.Public().(ed25519.PublicKey), certPublic) {
		return nil, nil, errors.New("key/certificate mismatch")
	}
	return cert, key, nil
}
func (s *Signer) install(root, intermediate domain.PKIAuthority, now time.Time) error {
	prepared, err := s.prepare(root, intermediate, now)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cache = prepared
	s.mu.Unlock()
	return nil
}
func (s *Signer) prepare(root, intermediate domain.PKIAuthority, now time.Time) (*cached, error) {
	rc, _, err := s.validate(root, now)
	if err != nil {
		return nil, err
	}
	ic, _, err := s.validate(intermediate, now)
	if err != nil {
		return nil, err
	}
	if intermediate.ParentAuthorityID == nil || *intermediate.ParentAuthorityID != root.ID || ic.CheckSignatureFrom(rc) != nil {
		return nil, errors.New("pki: invalid hierarchy")
	}
	if now.Before(rc.NotBefore) || !now.Before(rc.NotAfter) || now.Before(ic.NotBefore) || !now.Before(ic.NotAfter) {
		return nil, errors.New("pki: hierarchy outside validity window")
	}
	return &cached{root: root, intermediate: intermediate, rootCert: rc, intermediateCert: ic}, nil
}

func (s *Signer) Sign(ctx context.Context, req base.SignRequest) (base.SignedCertificate, error) {
	return s.SignGateway(ctx, req)
}

func (s *Signer) SignGateway(ctx context.Context, req base.SignRequest) (base.SignedCertificate, error) {
	if s.repo != nil {
		if err := s.EnsureHierarchy(ctx); err != nil {
			return base.SignedCertificate{}, err
		}
	}
	validated, err := base.ValidateCSR(req.CSR.DER(), req.GatewayID)
	if err != nil || validated.DERHash() != req.CSR.DERHash() {
		return base.SignedCertificate{}, errors.New("pki: invalid CSR")
	}
	s.mu.RLock()
	c := s.cache
	s.mu.RUnlock()
	if c == nil {
		return base.SignedCertificate{}, errors.New("pki: hierarchy not initialized")
	}
	if req.AuthorityID != "" && req.AuthorityID != c.intermediate.ID {
		return base.SignedCertificate{}, errors.New("pki: requested authority is not active")
	}
	now := s.now().UTC()
	if now.Before(c.intermediateCert.NotBefore) || !now.Before(c.intermediateCert.NotAfter) {
		return base.SignedCertificate{}, errors.New("pki: issuer outside validity")
	}
	issuer, intermediateKey, err := s.validate(c.intermediate, now)
	if err != nil || issuer.CheckSignatureFrom(c.rootCert) != nil {
		return base.SignedCertificate{}, errors.New("pki: cached issuer validation failed")
	}
	t, err := base.NewLeafTemplate(req.GatewayID, validated, s.cfg.Policy, now, c.intermediateCert.NotAfter, s.random)
	if err != nil {
		return base.SignedCertificate{}, err
	}
	csr, _ := x509.ParseCertificateRequest(validated.DER())
	der, err := x509.CreateCertificate(s.random, t, c.intermediateCert, csr.PublicKey, intermediateKey)
	if err != nil {
		return base.SignedCertificate{}, err
	}
	fp := sha256.Sum256(der)
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return base.SignedCertificate{}, err
	}
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), []byte(c.intermediate.CertificatePEM)...)
	return base.SignedCertificate{DER: der, ChainPEM: chain, TrustBundlePEM: []byte(c.root.CertificatePEM), Fingerprint: fp[:], AuthorityID: c.intermediate.ID, Serial: leaf.SerialNumber, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter}, nil
}
func (s *Signer) TrustBundle() ([]byte, error) {
	if s.repo != nil {
		if err := s.EnsureHierarchy(context.Background()); err != nil {
			return nil, err
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cache == nil {
		return nil, errors.New("pki: hierarchy not initialized")
	}
	return []byte(s.cache.root.CertificatePEM), nil
}

type mysqlTransactions struct{ db *sql.DB }
type mysqlAuthorityRepository struct{ tx *sql.Tx }

func (m mysqlTransactions) WithHierarchyLock(ctx context.Context, fn func(AuthorityRepository) error) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "SELECT id FROM pki_rotation_lock WHERE id=1 FOR UPDATE"); err != nil {
		return err
	}
	if err = fn(mysqlAuthorityRepository{tx: tx}); err != nil {
		return err
	}
	return tx.Commit()
}
func (m mysqlAuthorityRepository) LoadActive(ctx context.Context, kind string) (domain.PKIAuthority, error) {
	return loadActive(ctx, m.tx, kind)
}
func (m mysqlAuthorityRepository) Insert(ctx context.Context, a domain.PKIAuthority) error {
	return insert(ctx, m.tx, a)
}
func (m mysqlAuthorityRepository) RetireActiveIntermediate(ctx context.Context, now int64) error {
	_, err := m.tx.ExecContext(ctx, "UPDATE pki_authorities SET status='retiring',updated_at=? WHERE kind='intermediate' AND status='active'", now)
	return err
}

func loadActive(ctx context.Context, tx *sql.Tx, kind string) (domain.PKIAuthority, error) {
	var a domain.PKIAuthority
	var parent sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT id,kind,parent_authority_id,status,certificate_pem,certificate_fingerprint,encrypted_private_key,private_key_nonce,encryption_key_id,not_before,not_after,created_at,updated_at FROM pki_authorities WHERE kind=? AND status='active' LIMIT 1 FOR UPDATE", kind).Scan(&a.ID, &a.Kind, &parent, &a.Status, &a.CertificatePEM, &a.CertificateFingerprint, &a.EncryptedPrivateKey, &a.PrivateKeyNonce, &a.EncryptionKeyID, &a.NotBefore, &a.NotAfter, &a.CreatedAt, &a.UpdatedAt)
	if parent.Valid {
		a.ParentAuthorityID = &parent.String
	}
	return a, err
}
func insert(ctx context.Context, tx *sql.Tx, a domain.PKIAuthority) error {
	var pk any
	if a.Kind == "intermediate" {
		pk = "root"
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO pki_authorities(id,kind,parent_authority_id,parent_kind,status,certificate_pem,certificate_fingerprint,encrypted_private_key,private_key_nonce,encryption_key_id,not_before,not_after,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", a.ID, a.Kind, a.ParentAuthorityID, pk, "active", a.CertificatePEM, a.CertificateFingerprint, a.EncryptedPrivateKey, a.PrivateKeyNonce, a.EncryptionKeyID, a.NotBefore, a.NotAfter, a.CreatedAt, a.UpdatedAt)
	return err
}
func millis(t time.Time) int64 { return t.UnixMilli() }
