package gatewayidentity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
)

const version = 1

type Config struct {
	Directory, GatewayID string
	BootstrapCA          []byte
}
type Pending struct {
	CSRDER    []byte
	PublicKey ed25519.PublicKey
}
type Installation struct {
	GatewayID                string
	ChainPEM, TrustBundlePEM []byte
	AuthorityID, Serial      string
	NotBefore, NotAfter      int64
}
type metadata struct {
	Version                                  int `json:"version"`
	Generation, GatewayID, Authority, Serial string
	NotBefore, NotAfter                      int64
}
type pendingMetadata struct {
	Version   int    `json:"version"`
	GatewayID string `json:"gateway_id"`
	CSRHash   string `json:"csr_sha256"`
}
type identity struct {
	generation string
	cert       *tls.Certificate
	expiry     time.Time
}

type Manager struct {
	cfg    Config
	mu     sync.Mutex
	active atomic.Pointer[identity]
}

func New(cfg Config) (*Manager, error) {
	if cfg.Directory == "" || pki.ValidateGatewayID(cfg.GatewayID) != nil {
		return nil, errors.New("gateway identity: invalid configuration")
	}
	if _, _, err := pki.CanonicalCertPool(cfg.BootstrapCA); err != nil {
		return nil, err
	}
	return &Manager{cfg: cfg}, nil
}

func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ensureDir(m.cfg.Directory); err != nil {
		return err
	}
	loaded, err := m.recover()
	if err != nil {
		return err
	}
	m.active.Store(loaded)
	if pending, pendingErr := m.loadPending(); pendingErr == nil && loaded.cert.Leaf != nil && loaded.cert.Leaf.PublicKey.(ed25519.PublicKey).Equal(pending.PublicKey) {
		if removeErr := os.RemoveAll(filepath.Join(m.cfg.Directory, "pending")); removeErr != nil {
			return removeErr
		}
		if syncErr := syncDir(m.cfg.Directory); syncErr != nil {
			return syncErr
		}
	}
	return nil
}
func (m *Manager) Ready() bool { _, err := m.GetClientCertificate(nil); return err == nil }
func (m *Manager) GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	id := m.active.Load()
	if id == nil || !time.Now().UTC().Before(id.expiry) {
		return nil, errors.New("gateway identity unavailable")
	}
	return id.cert, nil
}

func (m *Manager) Prepare() (Pending, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ensureDir(m.cfg.Directory); err != nil {
		return Pending{}, err
	}
	if _, statErr := os.Stat(filepath.Join(m.cfg.Directory, "pending")); statErr == nil {
		return m.loadPending()
	} else if !os.IsNotExist(statErr) {
		return Pending{}, statErr
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Pending{}, err
	}
	u, _ := url.Parse("spiffe://quick-wa/gateway/" + m.cfg.GatewayID)
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: m.cfg.GatewayID}, URIs: []*url.URL{u}}, private)
	if err != nil {
		return Pending{}, err
	}
	hash := sha256.Sum256(csr)
	meta, _ := json.Marshal(pendingMetadata{Version: version, GatewayID: m.cfg.GatewayID, CSRHash: fmt.Sprintf("%x", hash[:])})
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(private)
	pendingDir := filepath.Join(m.cfg.Directory, ".pending.tmp")
	_ = os.RemoveAll(pendingDir)
	if err = os.Mkdir(pendingDir, 0o700); err != nil {
		return Pending{}, err
	}
	for _, f := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{{"key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o600}, {"csr.der", csr, 0o644}, {"metadata.json", append(meta, '\n'), 0o644}} {
		if err = writeSync(filepath.Join(pendingDir, f.name), f.data, f.mode); err != nil {
			return Pending{}, err
		}
	}
	if err = syncDir(pendingDir); err != nil {
		return Pending{}, err
	}
	if err = os.Rename(pendingDir, filepath.Join(m.cfg.Directory, "pending")); err != nil {
		return Pending{}, err
	}
	if err = syncDir(m.cfg.Directory); err != nil {
		return Pending{}, err
	}
	return Pending{CSRDER: append([]byte(nil), csr...), PublicKey: public}, nil
}

func (m *Manager) loadPending() (Pending, error) {
	d := filepath.Join(m.cfg.Directory, "pending")
	if info, err := os.Stat(d); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return Pending{}, errors.New("gateway identity: invalid pending directory permissions")
	}
	if info, err := os.Stat(filepath.Join(d, "key.pem")); err != nil || info.Mode().Perm() != 0o600 {
		return Pending{}, errors.New("gateway identity: invalid pending key permissions")
	}
	for _, name := range []string{"csr.der", "metadata.json"} {
		if info, err := os.Stat(filepath.Join(d, name)); err != nil || info.Mode().Perm() != 0o644 {
			return Pending{}, errors.New("gateway identity: invalid pending file permissions")
		}
	}
	keyPEM, err := os.ReadFile(filepath.Join(d, "key.pem"))
	if err != nil {
		return Pending{}, err
	}
	csr, err := os.ReadFile(filepath.Join(d, "csr.der"))
	if err != nil {
		return Pending{}, err
	}
	metaBytes, err := os.ReadFile(filepath.Join(d, "metadata.json"))
	if err != nil {
		return Pending{}, err
	}
	var meta pendingMetadata
	if json.Unmarshal(metaBytes, &meta) != nil || meta.Version != version || meta.GatewayID != m.cfg.GatewayID {
		return Pending{}, errors.New("gateway identity: invalid pending metadata")
	}
	block, rest := pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return Pending{}, errors.New("gateway identity: invalid pending key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	private, ok := parsed.(ed25519.PrivateKey)
	if err != nil || !ok {
		return Pending{}, errors.New("gateway identity: invalid pending key")
	}
	validated, err := pki.ValidateCSR(csr, m.cfg.GatewayID)
	if err != nil {
		return Pending{}, err
	}
	hash := validated.DERHash()
	if meta.CSRHash != fmt.Sprintf("%x", hash[:]) {
		return Pending{}, errors.New("gateway identity: pending hash mismatch")
	}
	parsedCSR, _ := x509.ParseCertificateRequest(csr)
	if !parsedCSR.PublicKey.(ed25519.PublicKey).Equal(private.Public()) {
		return Pending{}, errors.New("gateway identity: pending key mismatch")
	}
	return Pending{CSRDER: append([]byte(nil), csr...), PublicKey: private.Public().(ed25519.PublicKey)}, nil
}

func (m *Manager) Install(in Installation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	pending, err := m.loadPending()
	if err != nil {
		return err
	}
	if in.GatewayID != m.cfg.GatewayID || len(in.GatewayID) > 64 || len(in.ChainPEM) == 0 || len(in.ChainPEM) > 64<<10 || len(in.TrustBundlePEM) == 0 || len(in.TrustBundlePEM) > 16<<10 || in.AuthorityID == "" || len(in.AuthorityID) > 128 || in.Serial == "" || len(in.Serial) > 128 || in.NotBefore <= 0 || in.NotAfter <= in.NotBefore {
		return errors.New("gateway identity: invalid enrollment response")
	}
	if !bytes.Equal(in.TrustBundlePEM, m.cfg.BootstrapCA) {
		return errors.New("gateway identity: enrollment trust bundle mismatch")
	}
	leafBlock, _ := pem.Decode(in.ChainPEM)
	if leafBlock == nil {
		return errors.New("gateway identity: invalid chain")
	}
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		return err
	}
	validated, err := pki.ValidateCSR(pending.CSRDER, m.cfg.GatewayID)
	if err != nil {
		return err
	}
	fp := sha256.Sum256(leaf.Raw)
	signed := pki.SignedCertificate{DER: leaf.Raw, ChainPEM: in.ChainPEM, TrustBundlePEM: in.TrustBundlePEM, Fingerprint: fp[:], AuthorityID: in.AuthorityID, Serial: leaf.SerialNumber, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter}
	if err = pki.ValidateSignedGateway(signed, validated, m.cfg.GatewayID, time.Now().UTC()); err != nil {
		return err
	}
	if leaf.SerialNumber.String() != in.Serial || leaf.NotBefore.UnixMilli() != in.NotBefore || leaf.NotAfter.UnixMilli() != in.NotAfter || !leaf.PublicKey.(ed25519.PublicKey).Equal(pending.PublicKey) {
		return errors.New("gateway identity: enrollment metadata mismatch")
	}
	keyPEM, err := os.ReadFile(filepath.Join(m.cfg.Directory, "pending", "key.pem"))
	if err != nil {
		return err
	}
	gen := "identity-" + ulid.Make().String()
	staging := filepath.Join(m.cfg.Directory, "."+gen+".tmp")
	if err = os.Mkdir(staging, 0o700); err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
			_ = os.RemoveAll(filepath.Join(m.cfg.Directory, gen))
		}
	}()
	meta, _ := json.Marshal(metadata{Version: version, Generation: gen, GatewayID: m.cfg.GatewayID, Authority: in.AuthorityID, Serial: in.Serial, NotBefore: in.NotBefore, NotAfter: in.NotAfter})
	for _, f := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{{"key.pem", keyPEM, 0o600}, {"chain.pem", in.ChainPEM, 0o644}, {"trust.pem", in.TrustBundlePEM, 0o644}, {"metadata.json", append(meta, '\n'), 0o644}} {
		if err = writeSync(filepath.Join(staging, f.name), f.data, f.mode); err != nil {
			return err
		}
	}
	if err = syncDir(staging); err != nil {
		return err
	}
	d := filepath.Join(m.cfg.Directory, gen)
	if err = os.Rename(staging, d); err != nil {
		return err
	}
	if err = syncDir(m.cfg.Directory); err != nil {
		return err
	}
	// Read the candidate back before publishing it. A failed candidate is never
	// referenced by current, so the incumbent remains usable.
	loaded, err := m.loadGeneration(gen)
	if err != nil {
		return err
	}
	previous, _ := readPointer(m.cfg.Directory, "current")
	if previous != "" {
		if err = publishPointer(m.cfg.Directory, "previous", previous); err != nil {
			return err
		}
	}
	if err = publishPointer(m.cfg.Directory, "current", gen); err != nil {
		return err
	}
	published = true
	m.active.Store(loaded)
	if err = os.RemoveAll(filepath.Join(m.cfg.Directory, "pending")); err != nil {
		return err
	}
	return syncDir(m.cfg.Directory)
}

func (m *Manager) recover() (*identity, error) {
	for _, pointer := range []string{"current", "previous"} {
		if gen, e := readPointer(m.cfg.Directory, pointer); e == nil {
			if id, e := m.loadGeneration(gen); e == nil {
				return id, nil
			}
		}
	}
	entries, err := os.ReadDir(m.cfg.Directory)
	if err != nil {
		return nil, err
	}
	var gens []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "identity-") {
			gens = append(gens, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(gens)))
	for _, gen := range gens {
		if id, e := m.loadGeneration(gen); e == nil {
			_ = publishPointer(m.cfg.Directory, "current", gen)
			return id, nil
		}
	}
	return nil, errors.New("gateway identity: no valid identity")
}
func (m *Manager) loadGeneration(gen string) (*identity, error) {
	if strings.Contains(gen, "/") || !strings.HasPrefix(gen, "identity-") {
		return nil, errors.New("gateway identity: invalid generation")
	}
	d := filepath.Join(m.cfg.Directory, gen)
	if info, e := os.Stat(d); e != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("gateway identity: invalid generation permissions")
	}
	if info, e := os.Stat(filepath.Join(d, "key.pem")); e != nil || info.Mode().Perm() != 0o600 {
		return nil, errors.New("gateway identity: invalid key permissions")
	}
	for _, name := range []string{"chain.pem", "trust.pem", "metadata.json"} {
		if info, e := os.Stat(filepath.Join(d, name)); e != nil || info.Mode().Perm() != 0o644 {
			return nil, errors.New("gateway identity: invalid identity file permissions")
		}
	}
	keyPEM, e := os.ReadFile(filepath.Join(d, "key.pem"))
	if e != nil {
		return nil, e
	}
	chain, e := os.ReadFile(filepath.Join(d, "chain.pem"))
	if e != nil {
		return nil, e
	}
	trust, e := os.ReadFile(filepath.Join(d, "trust.pem"))
	if e != nil {
		return nil, e
	}
	metaBytes, e := os.ReadFile(filepath.Join(d, "metadata.json"))
	if e != nil {
		return nil, e
	}
	var meta metadata
	if json.Unmarshal(metaBytes, &meta) != nil || meta.Version != version || meta.Generation != gen || meta.GatewayID != m.cfg.GatewayID || !bytes.Equal(trust, m.cfg.BootstrapCA) {
		return nil, errors.New("gateway identity: invalid metadata")
	}
	cert, e := tls.X509KeyPair(chain, keyPEM)
	if e != nil {
		return nil, e
	}
	leaf, e := x509.ParseCertificate(cert.Certificate[0])
	if e != nil {
		return nil, e
	}
	cert.Leaf = leaf
	if leaf.SerialNumber.String() != meta.Serial || leaf.NotBefore.UnixMilli() != meta.NotBefore || leaf.NotAfter.UnixMilli() != meta.NotAfter || !time.Now().UTC().Before(leaf.NotAfter) {
		return nil, errors.New("gateway identity: invalid certificate metadata")
	}
	private, ok := cert.PrivateKey.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("gateway identity: invalid private key")
	}
	u, _ := url.Parse("spiffe://quick-wa/gateway/" + m.cfg.GatewayID)
	csrDER, e := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: m.cfg.GatewayID}, URIs: []*url.URL{u}}, private)
	if e != nil {
		return nil, e
	}
	validated, e := pki.ValidateCSR(csrDER, m.cfg.GatewayID)
	if e != nil {
		return nil, e
	}
	fingerprint := sha256.Sum256(leaf.Raw)
	signed := pki.SignedCertificate{DER: leaf.Raw, ChainPEM: chain, TrustBundlePEM: trust, Fingerprint: fingerprint[:], AuthorityID: meta.Authority, Serial: leaf.SerialNumber, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter}
	if e = pki.ValidateSignedGateway(signed, validated, m.cfg.GatewayID, time.Now().UTC()); e != nil {
		return nil, e
	}
	return &identity{generation: gen, cert: &cert, expiry: leaf.NotAfter}, nil
}

func ensureDir(path string) error {
	if e := os.MkdirAll(path, 0o700); e != nil {
		return e
	}
	return os.Chmod(path, 0o700)
}
func writeSync(path string, data []byte, mode os.FileMode) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if e != nil {
		return e
	}
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	return ce
}
func syncDir(path string) error {
	d, e := os.Open(path)
	if e != nil {
		return e
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
func publishPointer(dir, name, value string) error {
	tmp := filepath.Join(dir, "."+name+".tmp")
	if e := writeSync(tmp, []byte(value+"\n"), 0o644); e != nil {
		return e
	}
	if e := os.Rename(tmp, filepath.Join(dir, name)); e != nil {
		return e
	}
	return syncDir(dir)
}
func readPointer(dir, name string) (string, error) {
	b, e := os.ReadFile(filepath.Join(dir, name))
	if e != nil {
		return "", e
	}
	v := strings.TrimSpace(string(b))
	if v == "" || strings.ContainsAny(v, "/\\") {
		return "", errors.New("gateway identity: invalid pointer")
	}
	return v, nil
}

func (m *Manager) BootstrapCA() []byte { return append([]byte(nil), m.cfg.BootstrapCA...) }
