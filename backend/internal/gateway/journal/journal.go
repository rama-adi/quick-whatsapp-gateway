// Package journal provides the gateway-local durable handoff log used before
// normalized events are acknowledged by the API. It intentionally owns a
// separate SQLite file and never opens or mutates whatsmeow's keystore.
package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"modernc.org/sqlite"
)

const (
	DefaultMaxBytes       int64 = 1 << 30
	DefaultMaxVolumeShare int64 = 25
	DefaultDegradedAt     int64 = 70
	DefaultPauseAt        int64 = 80
	DefaultCriticalAt     int64 = 90
	DefaultBatchEntries         = 256
	DefaultBatchBytes     int64 = 1 << 20
)

var (
	ErrCorrupt    = errors.New("gateway journal corrupt")
	ErrCapacity   = errors.New("gateway journal capacity exceeded")
	ErrUnknownAck = errors.New("gateway journal acknowledgement exceeds stored entries")
)

type CapacityState string

const (
	CapacityHealthy  CapacityState = "healthy"
	CapacityDegraded CapacityState = "degraded"
	CapacityPaused   CapacityState = "paused"
	CapacityCritical CapacityState = "critical"
)

// Config values left at zero use the Section 17 locked defaults. VolumeBytes
// is normally discovered from the journal directory; tests and constrained
// deployments may provide it explicitly.
type Config struct {
	MaxBytes       int64
	VolumeBytes    int64
	MaxVolumeShare int64
	DegradedAt     int64
	PauseAt        int64
	CriticalAt     int64
}

func DefaultConfig() Config {
	return Config{
		MaxBytes:       DefaultMaxBytes,
		MaxVolumeShare: DefaultMaxVolumeShare,
		DegradedAt:     DefaultDegradedAt,
		PauseAt:        DefaultPauseAt,
		CriticalAt:     DefaultCriticalAt,
	}
}

func (c Config) normalized(volumeBytes int64) (Config, error) {
	defaults := DefaultConfig()
	if c.MaxBytes == 0 {
		c.MaxBytes = defaults.MaxBytes
	}
	if c.MaxVolumeShare == 0 {
		c.MaxVolumeShare = defaults.MaxVolumeShare
	}
	if c.DegradedAt == 0 {
		c.DegradedAt = defaults.DegradedAt
	}
	if c.PauseAt == 0 {
		c.PauseAt = defaults.PauseAt
	}
	if c.CriticalAt == 0 {
		c.CriticalAt = defaults.CriticalAt
	}
	if c.VolumeBytes == 0 {
		c.VolumeBytes = volumeBytes
	}
	boundsValid := c.MaxBytes > 0 && c.VolumeBytes > 0 && c.MaxVolumeShare > 0 && c.MaxVolumeShare <= 100
	thresholdsOrdered := c.DegradedAt > 0 &&
		c.DegradedAt < c.PauseAt &&
		c.PauseAt < c.CriticalAt &&
		c.CriticalAt < 100
	if !boundsValid || !thresholdsOrdered {
		return Config{}, errors.New("invalid gateway journal capacity configuration")
	}
	if c.MaxBytes > c.VolumeBytes*c.MaxVolumeShare/100 {
		return Config{}, fmt.Errorf(
			"gateway journal cap %d exceeds %d%% of volume budget %d",
			c.MaxBytes,
			c.MaxVolumeShare,
			c.VolumeBytes,
		)
	}
	return c, nil
}

// Entry is an opaque normalized-event payload with a stable event ID. Seq is
// assigned only once the append transaction commits and orders replay.
type Entry struct {
	Seq       uint64
	EventID   string
	Payload   []byte
	CreatedAt time.Time
}

type Metrics struct {
	Entries         int
	Bytes           int64
	OldestUnackedAt *time.Time
	AckedThrough    uint64
	State           CapacityState
}

type Journal struct {
	db  *sql.DB
	cfg Config
}

// Open creates or opens a durable WAL-mode journal and verifies an existing
// database before it is used for event acknowledgement state.
func Open(ctx context.Context, path string, cfg Config) (*Journal, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("gateway journal path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create journal directory: %w", err)
	}
	volume, err := filesystemBytes(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	cfg, err = cfg.normalized(volume)
	if err != nil {
		return nil, err
	}
	info, statErr := os.Stat(path)
	switch {
	case statErr == nil && !info.Mode().IsRegular():
		return nil, errors.New("gateway journal path is not a regular file")
	case statErr != nil && !errors.Is(statErr, fs.ErrNotExist):
		return nil, fmt.Errorf("stat gateway journal: %w", statErr)
	}
	dsn := "file:" + path
	dsn += "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open gateway journal: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := integrityCheck(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS journal_entries (
seq INTEGER PRIMARY KEY AUTOINCREMENT,
event_id TEXT NOT NULL UNIQUE,
payload BLOB NOT NULL,
created_at INTEGER NOT NULL
); CREATE INDEX IF NOT EXISTS idx_journal_entries_seq ON journal_entries(seq);
CREATE TABLE IF NOT EXISTS journal_meta (
id INTEGER PRIMARY KEY CHECK (id=1),
acked_through INTEGER NOT NULL
); INSERT OR IGNORE INTO journal_meta (id, acked_through) VALUES (1, 0);
CREATE TABLE IF NOT EXISTS command_results (
command_id TEXT PRIMARY KEY,
session_id TEXT NOT NULL,
status TEXT NOT NULL,
wa_message_id TEXT NOT NULL DEFAULT '',
error TEXT NOT NULL DEFAULT '',
updated_at INTEGER NOT NULL,
expires_at INTEGER NOT NULL
);`); err != nil {
		_ = db.Close()
		return nil, classifySQLiteError(fmt.Errorf("initialize gateway journal: %w", err))
	}
	return &Journal{db: db, cfg: cfg}, nil
}

func filesystemBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("stat gateway journal volume: %w", err)
	}
	return int64(stat.Blocks) * int64(stat.Bsize), nil
}

func integrityCheck(ctx context.Context, db *sql.DB) error {
	var result string
	err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result)
	switch {
	case err != nil:
		return fmt.Errorf("%w: quick_check: %v", ErrCorrupt, err)
	case result != "ok":
		return fmt.Errorf("%w: quick_check returned %q", ErrCorrupt, result)
	}
	return nil
}

func newEntry(seq int64, eventID string, payload []byte, createdMS int64) Entry {
	return Entry{
		Seq:       uint64(seq),
		EventID:   eventID,
		Payload:   append([]byte(nil), payload...),
		CreatedAt: time.UnixMilli(createdMS).UTC(),
	}
}

func (j *Journal) Append(
	ctx context.Context,
	eventID string,
	payload []byte,
	createdAt time.Time,
) (Entry, bool, error) {
	if eventID == "" || len(payload) == 0 || createdAt.IsZero() {
		return Entry{}, false, errors.New("gateway journal event id, payload, and time are required")
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, false, classifySQLiteError(err)
	}
	defer tx.Rollback()
	var seq int64
	var existing []byte
	var createdMS int64
	err = tx.QueryRowContext(
		ctx,
		`SELECT seq, payload, created_at FROM journal_entries WHERE event_id=?`,
		eventID,
	).Scan(&seq, &existing, &createdMS)
	if err == nil {
		if err = tx.Commit(); err != nil {
			return Entry{}, false, classifySQLiteError(err)
		}
		return newEntry(seq, eventID, existing, createdMS), false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, classifySQLiteError(err)
	}
	metrics, err := metricsTx(ctx, tx, j.cfg)
	if err != nil {
		return Entry{}, false, classifySQLiteError(err)
	}
	projectedBytes := metrics.Bytes + int64(len(payload))
	if projectedBytes > j.cfg.MaxBytes {
		return Entry{}, false, fmt.Errorf("%w: %d > %d", ErrCapacity, projectedBytes, j.cfg.MaxBytes)
	}
	createdMS = createdAt.UTC().UnixMilli()
	result, err := tx.ExecContext(
		ctx,
		`INSERT INTO journal_entries (event_id, payload, created_at) VALUES (?, ?, ?)`,
		eventID,
		payload,
		createdMS,
	)
	if err != nil {
		return Entry{}, false, classifySQLiteError(err)
	}
	seq, err = result.LastInsertId()
	if err != nil {
		return Entry{}, false, classifySQLiteError(err)
	}
	if err = tx.Commit(); err != nil {
		return Entry{}, false, classifySQLiteError(err)
	}
	return newEntry(seq, eventID, payload, createdMS), true, nil
}

// ReadUnacked returns the oldest entries in strict sequence order, stopping
// before either caller-supplied batch limit would be exceeded.
func (j *Journal) ReadUnacked(ctx context.Context, maxEntries int, maxBytes int64) ([]Entry, error) {
	if maxEntries <= 0 || maxBytes <= 0 {
		return nil, errors.New("gateway journal read limits must be positive")
	}
	rows, err := j.db.QueryContext(
		ctx,
		`SELECT seq, event_id, payload, created_at FROM journal_entries ORDER BY seq ASC LIMIT ?`,
		maxEntries,
	)
	if err != nil {
		return nil, classifySQLiteError(err)
	}
	defer rows.Close()
	out := make([]Entry, 0, maxEntries)
	var used int64
	for rows.Next() {
		var entry Entry
		var seq, createdMS int64
		if err := rows.Scan(&seq, &entry.EventID, &entry.Payload, &createdMS); err != nil {
			return nil, classifySQLiteError(err)
		}
		if len(out) > 0 && used+int64(len(entry.Payload)) > maxBytes {
			break
		}
		entry.Seq, entry.CreatedAt = uint64(seq), time.UnixMilli(createdMS).UTC()
		entry.Payload = append([]byte(nil), entry.Payload...)
		out, used = append(out, entry), used+int64(len(entry.Payload))
	}
	if err := rows.Err(); err != nil {
		return nil, classifySQLiteError(err)
	}
	return out, nil
}

func (j *Journal) ReadDefaultBatch(ctx context.Context) ([]Entry, error) {
	return j.ReadUnacked(ctx, DefaultBatchEntries, DefaultBatchBytes)
}

// Ack atomically advances the durable acknowledgement watermark and removes
// every entry through it. Repeated acknowledgements are harmless; future ones
// are rejected rather than silently dropping subsequently appended events.
func (j *Journal) Ack(ctx context.Context, through uint64) error {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteError(err)
	}
	defer tx.Rollback()
	var watermark int64
	if err = tx.QueryRowContext(ctx, `SELECT acked_through FROM journal_meta WHERE id=1`).Scan(&watermark); err != nil {
		return classifySQLiteError(err)
	}
	if through <= uint64(watermark) {
		return tx.Commit()
	}
	var maximum int64
	maximumQuery := `SELECT COALESCE(MAX(seq), ?) FROM journal_entries`
	if err = tx.QueryRowContext(ctx, maximumQuery, watermark).Scan(&maximum); err != nil {
		return classifySQLiteError(err)
	}
	if through > uint64(maximum) {
		return fmt.Errorf("%w: %d > %d", ErrUnknownAck, through, maximum)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM journal_entries WHERE seq <= ?`, through); err != nil {
		return classifySQLiteError(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE journal_meta SET acked_through=? WHERE id=1`, through); err != nil {
		return classifySQLiteError(err)
	}
	if err = tx.Commit(); err != nil {
		return classifySQLiteError(err)
	}
	return nil
}

func (j *Journal) Metrics(ctx context.Context) (Metrics, error) {
	// Heartbeat cadence is the natural sweep point for expired command results.
	// A failed prune only delays space reuse until the next cycle, so it never
	// masks live pressure metrics or flips readiness.
	_ = j.PruneCommands(ctx, time.Now())
	return metricsDB(ctx, j.db, j.cfg)
}

func metricsDB(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, cfg Config) (Metrics, error) {
	const usageQuery = `SELECT COUNT(*), COALESCE(SUM(length(payload)),0), MIN(created_at),` +
		` (SELECT acked_through FROM journal_meta WHERE id=1) FROM journal_entries`
	var count, bytes, watermark int64
	var oldest sql.NullInt64
	if err := q.QueryRowContext(ctx, usageQuery).Scan(&count, &bytes, &oldest, &watermark); err != nil {
		return Metrics{}, classifySQLiteError(err)
	}
	m := Metrics{Entries: int(count), Bytes: bytes, AckedThrough: uint64(watermark), State: stateFor(bytes, cfg)}
	if oldest.Valid {
		value := time.UnixMilli(oldest.Int64).UTC()
		m.OldestUnackedAt = &value
	}
	return m, nil
}

func metricsTx(ctx context.Context, tx *sql.Tx, cfg Config) (Metrics, error) {
	return metricsDB(ctx, tx, cfg)
}

func stateFor(bytes int64, cfg Config) CapacityState {
	percent := bytes * 100 / cfg.MaxBytes
	if percent >= cfg.CriticalAt {
		return CapacityCritical
	}
	if percent >= cfg.PauseAt {
		return CapacityPaused
	}
	if percent >= cfg.DegradedAt {
		return CapacityDegraded
	}
	return CapacityHealthy
}

func (j *Journal) Checkpoint(ctx context.Context) error {
	if _, err := j.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return classifySQLiteError(err)
	}
	return nil
}

func (j *Journal) Close() error {
	if j == nil || j.db == nil {
		return nil
	}
	checkpointErr := j.Checkpoint(context.Background())
	closeErr := j.db.Close()
	if checkpointErr != nil && closeErr != nil {
		return errors.Join(checkpointErr, closeErr)
	}
	if checkpointErr != nil {
		return checkpointErr
	}
	return closeErr
}

func classifySQLiteError(err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == 13 {
		return fmt.Errorf("%w: %v", ErrCapacity, err)
	}
	return err
}
