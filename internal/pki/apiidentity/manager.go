package apiidentity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
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

const formatVersion = 1

type Config struct {
	Directory   string
	RenewBefore time.Duration
}

type metadata struct {
	Version    int    `json:"version"`
	Generation string `json:"generation"`
	Authority  string `json:"authority_id"`
	Serial     string `json:"serial_number"`
	NotBefore  int64  `json:"not_before_unix_ms"`
	NotAfter   int64  `json:"not_after_unix_ms"`
}

type loadedIdentity struct {
	generation string
	key        ed25519.PrivateKey
	cert       *tls.Certificate
	notBefore  time.Time
	notAfter   time.Time
}

type Manager struct {
	cfg    Config
	signer pki.APIIdentitySigner
	now    func() time.Time
	random io.Reader
	mu     sync.Mutex
	active atomic.Pointer[loadedIdentity]
}

func New(cfg Config, signer pki.APIIdentitySigner) (*Manager, error) {
	if cfg.Directory == "" || cfg.RenewBefore <= 0 || signer == nil {
		return nil, errors.New("API identity: invalid configuration")
	}
	return &Manager{cfg: cfg, signer: signer, now: time.Now, random: rand.Reader}, nil
}

func (m *Manager) Ensure(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	incumbent := m.active.Load()
	if incumbent == nil {
		loaded, err := m.loadOrRecover(now)
		if err == nil {
			incumbent = loaded
			m.active.Store(loaded)
		}
	}
	incumbentUsable := incumbent != nil &&
		now.Before(incumbent.notAfter) &&
		now.Add(m.cfg.RenewBefore).Before(incumbent.notAfter)
	if incumbentUsable {
		return nil
	}
	issued, err := m.issue(ctx, now, incumbent)
	if err != nil {
		return err
	}
	m.active.Store(issued)
	return nil
}

func (m *Manager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	identity := m.active.Load()
	if identity == nil {
		return nil, errors.New("API identity unavailable")
	}
	now := m.now().UTC()
	if now.Before(identity.notBefore) || !now.Before(identity.notAfter) {
		return nil, errors.New("API identity expired or not yet valid")
	}
	return identity.cert, nil
}

func (m *Manager) Ready() bool {
	_, err := m.GetCertificate(nil)
	return err == nil
}

func (m *Manager) Expiry() time.Time {
	identity := m.active.Load()
	if identity == nil {
		return time.Time{}
	}
	return identity.notAfter
}

func (m *Manager) loadOrRecover(now time.Time) (*loadedIdentity, error) {
	if err := ensureDirectory(m.cfg.Directory, 0o700); err != nil {
		return nil, err
	}
	if generation, err := readCurrent(m.cfg.Directory); err == nil {
		if identity, loadErr := m.loadGeneration(generation, now); loadErr == nil {
			return identity, nil
		}
	}
	entries, err := os.ReadDir(m.cfg.Directory)
	if err != nil {
		return nil, err
	}
	var generations []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "identity-") {
			generations = append(generations, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(generations)))
	for _, generation := range generations {
		identity, loadErr := m.loadGeneration(generation, now)
		if loadErr != nil {
			continue
		}
		if err = publishCurrent(m.cfg.Directory, generation); err != nil {
			return nil, err
		}
		m.cleanupRecoveredGenerations(generation, generations, now)
		return identity, nil
	}
	return nil, errors.New("API identity: no valid persisted generation")
}

func (m *Manager) cleanupRecoveredGenerations(current string, generations []string, now time.Time) {
	previousRetained := false
	for _, generation := range generations {
		if generation == current {
			continue
		}
		_, err := m.loadGeneration(generation, now)
		if err == nil && !previousRetained {
			previousRetained = true
			continue
		}
		_ = os.RemoveAll(filepath.Join(m.cfg.Directory, generation))
	}
	_ = syncDirectory(m.cfg.Directory)
}

func (m *Manager) issue(ctx context.Context, now time.Time, incumbent *loadedIdentity) (*loadedIdentity, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(m.random)
	if err != nil {
		return nil, err
	}
	signed, err := m.signer.SignAPI(ctx, pki.APISignRequest{PublicKey: publicKey})
	if err != nil {
		return nil, err
	}
	if err = pki.ValidateSignedAPI(signed, publicKey, now); err != nil {
		return nil, err
	}
	identifier, err := ulid.New(ulid.Timestamp(now), m.random)
	if err != nil {
		return nil, err
	}
	generation := "identity-" + identifier.String()
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	meta := metadata{
		Version:    formatVersion,
		Generation: generation,
		Authority:  signed.AuthorityID,
		Serial:     signed.Serial.String(),
		NotBefore:  signed.NotBefore.UnixMilli(),
		NotAfter:   signed.NotAfter.UnixMilli(),
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	if err = ensureDirectory(m.cfg.Directory, 0o700); err != nil {
		return nil, err
	}
	directory := filepath.Join(m.cfg.Directory, generation)
	if err = os.Mkdir(directory, 0o700); err != nil {
		return nil, err
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{name: "key.pk8", data: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), mode: 0o600},
		{name: "chain.pem", data: signed.ChainPEM, mode: 0o644},
		{name: "trust.pem", data: signed.TrustBundlePEM, mode: 0o644},
		{name: "metadata.json", data: append(metaJSON, '\n'), mode: 0o644},
	}
	for _, file := range files {
		if err = writeSynced(filepath.Join(directory, file.name), file.data, file.mode); err != nil {
			return nil, err
		}
	}
	if err = syncDirectory(directory); err != nil {
		return nil, err
	}
	if err = syncDirectory(m.cfg.Directory); err != nil {
		return nil, err
	}
	identity, err := m.loadGeneration(generation, now)
	if err != nil {
		return nil, err
	}
	if err = publishCurrent(m.cfg.Directory, generation); err != nil {
		return nil, err
	}
	previous := ""
	if incumbent != nil {
		previous = incumbent.generation
	}
	m.prune(generation, previous)
	return identity, nil
}

func (m *Manager) loadGeneration(generation string, now time.Time) (*loadedIdentity, error) {
	if !strings.HasPrefix(generation, "identity-") || strings.Contains(generation, string(filepath.Separator)) {
		return nil, errors.New("API identity: invalid generation")
	}
	directory := filepath.Join(m.cfg.Directory, generation)
	if err := requireMode(directory, 0o700); err != nil {
		return nil, err
	}
	keyPEM, err := readMode(filepath.Join(directory, "key.pk8"), 0o600)
	if err != nil {
		return nil, err
	}
	chain, err := readMode(filepath.Join(directory, "chain.pem"), 0o644)
	if err != nil {
		return nil, err
	}
	trust, err := readMode(filepath.Join(directory, "trust.pem"), 0o644)
	if err != nil {
		return nil, err
	}
	metaBytes, err := readMode(filepath.Join(directory, "metadata.json"), 0o644)
	if err != nil {
		return nil, err
	}
	var meta metadata
	if json.Unmarshal(metaBytes, &meta) != nil || meta.Version != formatVersion || meta.Generation != generation {
		return nil, errors.New("API identity: invalid metadata")
	}
	keyBlock, rest := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" || len(rest) != 0 {
		return nil, errors.New("API identity: invalid private key")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	privateKey, ok := parsedKey.(ed25519.PrivateKey)
	if err != nil || !ok {
		return nil, errors.New("API identity: private key is not Ed25519")
	}
	leafBlock, _ := pem.Decode(chain)
	if leafBlock == nil {
		return nil, errors.New("API identity: invalid chain")
	}
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256Sum(leaf.Raw)
	signed := pki.SignedCertificate{
		DER:            leaf.Raw,
		ChainPEM:       chain,
		TrustBundlePEM: trust,
		Fingerprint:    fingerprint,
		AuthorityID:    meta.Authority,
		Serial:         leaf.SerialNumber,
		NotBefore:      leaf.NotBefore,
		NotAfter:       leaf.NotAfter,
	}
	metadataMismatch := meta.Serial != leaf.SerialNumber.String() ||
		meta.NotBefore != leaf.NotBefore.UnixMilli() ||
		meta.NotAfter != leaf.NotAfter.UnixMilli()
	if metadataMismatch || pki.ValidateSignedAPI(signed, privateKey.Public().(ed25519.PublicKey), now) != nil {
		return nil, errors.New("API identity: persisted identity validation failed")
	}
	tlsCert, err := tls.X509KeyPair(chain, keyPEM)
	if err != nil {
		return nil, err
	}
	tlsCert.Leaf = leaf
	return &loadedIdentity{
		generation: generation,
		key:        privateKey,
		cert:       &tlsCert,
		notBefore:  leaf.NotBefore,
		notAfter:   leaf.NotAfter,
	}, nil
}

func (m *Manager) prune(current, previous string) {
	entries, err := os.ReadDir(m.cfg.Directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "identity-") {
			generation := entry.Name()
			if generation == current || generation == previous {
				continue
			}
			_ = os.RemoveAll(filepath.Join(m.cfg.Directory, generation))
		}
	}
	_ = syncDirectory(m.cfg.Directory)
}

func readCurrent(directory string) (string, error) {
	b, err := readMode(filepath.Join(directory, "current"), 0o644)
	return strings.TrimSpace(string(b)), err
}
func publishCurrent(directory, generation string) error {
	temporary := filepath.Join(directory, ".current.tmp")
	if err := writeSynced(temporary, []byte(generation+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, filepath.Join(directory, "current")); err != nil {
		return err
	}
	return syncDirectory(directory)
}
func ensureDirectory(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return requireMode(path, mode)
}
func writeSynced(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errors.Is(err, os.ErrExist) {
		if err = os.Remove(path); err == nil {
			file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		}
	}
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func readMode(path string, mode os.FileMode) ([]byte, error) {
	if err := requireMode(path, mode); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
func requireMode(path string, mode os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != mode {
		return fmt.Errorf("API identity: %s mode is %o, want %o", path, info.Mode().Perm(), mode)
	}
	return nil
}
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
