package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// ContactRepo is the repository for whatsapp_contacts — per-account "found user"
// records powering the Contacts feature (§5/§7). Keyed by (session_id, lid).
type ContactRepo struct {
	db dbExecQuerier
}

// NewContactRepo constructs a ContactRepo.
func NewContactRepo(db dbExecQuerier) *ContactRepo { return &ContactRepo{db: db} }

const contactCols = `id, session_id, lid, phone, seen_in_dm, dm_first_seen_at,
	dm_last_seen_at, message_count, first_seen_at, last_seen_at`

func scanContact(s rowScanner) (domain.Contact, error) {
	var c domain.Contact
	err := s.Scan(
		&c.ID, &c.SessionID, &c.LID, &c.Phone, &c.SeenInDM, &c.DMFirstSeenAt,
		&c.DMLastSeenAt, &c.MessageCount, &c.FirstSeenAt, &c.LastSeenAt,
	)
	if err != nil {
		return domain.Contact{}, err
	}
	return c, nil
}

// Upsert inserts or updates a contact by (session_id, lid). On conflict it ORs
// in seen_in_dm, fills the DM-first-seen on the first DM sighting (COALESCE),
// and advances dm_last_seen_at / last_seen_at. message_count is bumped
// separately by BumpSeen, so it is not touched here (insert seeds it from the
// struct). first_seen_at is preserved.
func (r *ContactRepo) Upsert(ctx context.Context, c domain.Contact) error {
	const q = `INSERT INTO whatsapp_contacts
(session_id, lid, phone, seen_in_dm, dm_first_seen_at, dm_last_seen_at, message_count,
 first_seen_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	phone            = COALESCE(VALUES(phone), phone),
	seen_in_dm       = seen_in_dm | VALUES(seen_in_dm),
	dm_first_seen_at = COALESCE(dm_first_seen_at, VALUES(dm_first_seen_at)),
	dm_last_seen_at  = COALESCE(VALUES(dm_last_seen_at), dm_last_seen_at),
	last_seen_at     = VALUES(last_seen_at)`
	if _, err := r.db.ExecContext(ctx, q,
		c.SessionID, c.LID, c.Phone, c.SeenInDM, c.DMFirstSeenAt, c.DMLastSeenAt,
		c.MessageCount, c.FirstSeenAt, c.LastSeenAt,
	); err != nil {
		return fmt.Errorf("store: upsert contact: %w", err)
	}
	return nil
}

// BumpSeen increments message_count and advances last_seen_at for an existing
// contact (called per inbound message after Upsert has ensured the row exists).
func (r *ContactRepo) BumpSeen(ctx context.Context, sessionID, lid string, lastSeenAt int64) error {
	const q = `UPDATE whatsapp_contacts
SET message_count = message_count + 1, last_seen_at = ?
WHERE session_id = ? AND lid = ?`
	res, err := r.db.ExecContext(ctx, q, lastSeenAt, sessionID, lid)
	if err != nil {
		return fmt.Errorf("store: bump contact: %w", err)
	}
	return affectedOrNotFound(res, "contact")
}

// Get fetches a single contact by (session_id, lid). Maps no-rows to not_found.
func (r *ContactRepo) Get(ctx context.Context, sessionID, lid string) (domain.Contact, error) {
	q := "SELECT " + contactCols + " FROM whatsapp_contacts WHERE session_id = ? AND lid = ?"
	c, err := scanContact(r.db.QueryRowContext(ctx, q, sessionID, lid))
	if err != nil {
		return domain.Contact{}, notFound(err, "contact")
	}
	return c, nil
}

// ContactFilter is the §11 GET /contacts filter set. Source "dm" restricts to
// contacts seen in a DM; GroupJID restricts to members of a group (joined via
// whatsapp_group_members); Q is a substring match against the resolved push
// name in whatsapp_identities.
type ContactFilter struct {
	Source   string // "" | "dm" | "group"
	GroupJID string // when Source == "group" (or standalone group filter)
	Q        string // free-text name search
}

// List returns a page of contacts for a session, applying ContactFilter and
// cursor pagination over the contact id. It joins identities for the name search
// and group_members for the group filter. Ordered by id ASC so the cursor is the
// last returned id.
func (r *ContactRepo) List(ctx context.Context, sessionID string, f ContactFilter, cursor string, limit int) (Page[domain.Contact], error) {
	afterID, err := parseCursor(cursor)
	if err != nil {
		return Page[domain.Contact]{}, err
	}
	limit = normLimit(limit)

	var (
		sb   strings.Builder
		args []any
	)
	sb.WriteString("SELECT ")
	// Qualify columns since we may join.
	sb.WriteString(prefixCols("c", contactCols))
	sb.WriteString(" FROM whatsapp_contacts c")

	if f.GroupJID != "" {
		// Restrict to contacts that are members of the given group on this session.
		sb.WriteString(` JOIN whatsapp_group_members gm
			ON gm.session_id = c.session_id AND gm.lid = c.lid AND gm.group_jid = ?`)
		args = append(args, f.GroupJID)
	}
	if f.Q != "" {
		// Name search lives on the global identity row.
		sb.WriteString(" LEFT JOIN whatsapp_identities i ON i.lid = c.lid")
	}

	sb.WriteString(" WHERE c.session_id = ? AND c.id > ?")
	args = append(args, sessionID, afterID)

	if f.Source == "dm" {
		sb.WriteString(" AND c.seen_in_dm = 1")
	}
	if f.Q != "" {
		sb.WriteString(" AND i.name LIKE ?")
		args = append(args, "%"+f.Q+"%")
	}

	sb.WriteString(" ORDER BY c.id ASC LIMIT ?")
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return Page[domain.Contact]{}, fmt.Errorf("store: list contacts: %w", err)
	}
	defer rows.Close()
	var out []domain.Contact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return Page[domain.Contact]{}, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.Contact]{}, err
	}
	return pageFrom(out, limit, func(c domain.Contact) uint64 { return c.ID }), nil
}
