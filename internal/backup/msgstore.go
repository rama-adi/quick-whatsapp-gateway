package backup

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // CGO-free SQLite driver, registered as "sqlite"
)

// DB is a read-only handle on a decrypted WhatsApp msgstore database. It detects
// the schema by feature (which tables/columns exist) rather than a version field
// — WhatsApp exposes no stable schema version — so a renamed or missing optional
// table degrades gracefully instead of failing the whole import.
type DB struct {
	db     *sql.DB
	caps   capabilities
	finger string
}

// capabilities records which tables exist and, for the tables this reader cares
// about, which columns exist.
type capabilities struct {
	tables map[string]bool
	cols   map[string]map[string]bool
}

func (c capabilities) hasTable(t string) bool { return c.tables[t] }

func (c capabilities) hasCol(t, col string) bool {
	cols, ok := c.cols[t]
	return ok && cols[col]
}

// tablesProbedForCols are the tables whose columns we inspect for graceful
// degradation. Probing every table's columns would be wasteful.
var tablesProbedForCols = []string{
	"message", "chat", "jid", "message_media", "message_location",
	"message_quoted", "message_mentions", "group_participant_user", "props",
}

// Open opens the decrypted SQLite file read-only and probes its schema. It errors
// if the file is not a recognizable WhatsApp msgstore (missing the core
// message/chat/jid trio).
func Open(path string) (*DB, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve sqlite path: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: absPath, RawQuery: "mode=ro&immutable=1"}).String()
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A backup reader is intentionally single-connection: it is immutable and
	// all iteration is sequential, so extra handles only waste file descriptors.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	caps, err := probe(sqlDB)
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	if !caps.hasTable("message") || !caps.hasTable("chat") || !caps.hasTable("jid") {
		sqlDB.Close()
		return nil, fmt.Errorf("unrecognized WhatsApp backup schema: missing core tables (message/chat/jid)")
	}
	return &DB{db: sqlDB, caps: caps, finger: fingerprint(sqlDB, caps)}, nil
}

// Close releases the underlying immutable SQLite handle. Callers own Close
// after every successful Open; failed Open calls close their partial handle.
func (d *DB) Close() error { return d.db.Close() }

// Fingerprint is an opaque schema identifier (WhatsApp build id + SQLite
// user_version + a hash of the detected table/column set) recorded on the import
// job for observability when WhatsApp reshapes the DB.
func (d *DB) Fingerprint() string { return d.finger }

// probe captures a consistent-enough immutable schema capability snapshot.
// Every rows handle is closed on scan and iteration failures before returning,
// preventing the single-connection reader from deadlocking later probes.
func probe(db *sql.DB) (capabilities, error) {
	ctx := context.Background()
	caps := capabilities{tables: map[string]bool{}, cols: map[string]map[string]bool{}}

	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return caps, fmt.Errorf("probe tables: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return caps, err
		}
		caps.tables[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return caps, err
	}
	rows.Close()

	for _, t := range tablesProbedForCols {
		if !caps.tables[t] {
			continue
		}
		cols, err := tableColumns(ctx, db, t)
		if err != nil {
			return caps, err
		}
		caps.cols[t] = cols
	}
	return caps, nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	// table is from our fixed allow-list, never user input — safe to interpolate.
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, fmt.Errorf("probe columns of %s: %w", table, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var (
			cid         int
			name, ctype string
			notNull, pk int
			dfltValue   sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// fingerprint combines optional WhatsApp build metadata with a deterministic
// hash of sorted detected tables and probed columns. Query failures are best
// effort because the capability hash alone still identifies a readable schema.
func fingerprint(db *sql.DB, caps capabilities) string {
	var buildID, userVersion string
	if caps.hasTable("props") {
		_ = db.QueryRow("SELECT value FROM props WHERE key='schema-maintainer/previous-run-build-id'").Scan(&buildID)
	}
	_ = db.QueryRow("PRAGMA user_version").Scan(&userVersion)

	// Hash the sorted table+column set so a schema change is visible in the
	// fingerprint without dumping the whole set.
	h := fnv.New32a()
	tbls := make([]string, 0, len(caps.tables))
	for t := range caps.tables {
		tbls = append(tbls, t)
	}
	sort.Strings(tbls)
	for _, t := range tbls {
		h.Write([]byte(t))
		h.Write([]byte{0})
		cols := make([]string, 0, len(caps.cols[t]))
		for col := range caps.cols[t] {
			cols = append(cols, col)
		}
		sort.Strings(cols)
		for _, col := range cols {
			h.Write([]byte(col))
			h.Write([]byte{0})
		}
		h.Write([]byte{0})
	}
	cap8 := fmt.Sprintf("%08x", h.Sum32())

	parts := []string{}
	if buildID != "" {
		parts = append(parts, "build="+buildID)
	}
	if userVersion != "" {
		parts = append(parts, "uv="+userVersion)
	}
	parts = append(parts, "caps="+cap8)
	return strings.Join(parts, ";")
}

// ---------------------------------------------------------------------------
// DTOs (decoupled from internal/domain — the service maps these onto domain types)
// ---------------------------------------------------------------------------

// Chat is one chat thread.
type Chat struct {
	JID           string
	Type          string // dm | group | newsletter | broadcast | status
	Name          string // group subject (empty for DMs)
	LastMessageAt int64  // epoch-ms; 0 when unknown
}

// Message is one chat message, already classified to a coarse type with its body
// resolved from text / media caption / location name.
type Message struct {
	WAMessageID     string
	ChatJID         string
	SenderLID       string // set when the sender JID is an "@lid"
	SenderJID       string // set when the sender JID is a phone JID
	FromMe          bool
	Type            string // text|image|video|audio|document|gif|sticker|location|contact
	Body            string
	QuotedMessageID string
	Mentions        []string // mentioned JIDs (raw_string)
	HasMedia        bool
	MediaMime       string
	MediaSize       int64
	MediaName       string
	TimestampMs     int64
}

// Identity is a sender/contact identity seeded from the backup's jid table.
type Identity struct {
	LID      string // set for "@lid" jids
	PhoneJID string // set for "@s.whatsapp.net" jids
	Phone    string // bare phone (user part of a phone jid)
	Name     string // best-effort display name (from mentions); empty when unknown
}

// Group is a group thread.
type Group struct {
	JID     string
	Subject string
}

// GroupMember is one group participant.
type GroupMember struct {
	GroupJID string
	LID      string // participant identifier as encountered (@lid or phone jid)
	Tag      string // per-group label
	Role     string // member | admin | superadmin
}

// ---------------------------------------------------------------------------
// Chat type classification
// ---------------------------------------------------------------------------

func chatTypeForServer(server string) string {
	switch server {
	case "g.us":
		return "group"
	case "newsletter":
		return "newsletter"
	case "broadcast":
		return "broadcast"
	case "status_me":
		return "status"
	default:
		return "dm" // s.whatsapp.net, lid, bot, ...
	}
}

// coarseType maps WhatsApp's numeric message_type to our string type. Codes are
// confirmed against a real msgstore; unknown codes return "" so the caller can
// decide to skip (no content) or treat as text (has a body).
func coarseType(code int) string {
	switch code {
	case 0:
		return "text"
	case 1, 42:
		return "image"
	case 2:
		return "audio"
	case 3, 43:
		return "video"
	case 13:
		return "gif"
	case 4:
		return "contact"
	case 5, 16:
		return "location"
	case 9:
		return "document"
	case 15, 20:
		return "sticker"
	default:
		return ""
	}
}
