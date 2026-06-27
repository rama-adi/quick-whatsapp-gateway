package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// ContactRepo backs the "found users" feature as a PROJECTION over the central
// identity table — there is no contacts table. A person is "found" by a session
// when they appear in that session's chats as a DM, or in its group memberships
// (§5/§7). Identities are global; the per-session DM/group signals scope the view.
type ContactRepo struct {
	db dbExecQuerier
}

// NewContactRepo constructs a ContactRepo.
func NewContactRepo(db dbExecQuerier) *ContactRepo { return &ContactRepo{db: db} }

// ContactFilter is the §11 GET /contacts filter set. Source "dm" restricts to
// people seen in a direct chat; "group" (or GroupJID) restricts to group members;
// Q is a substring match against the resolved push name in whatsapp_identities.
type ContactFilter struct {
	Source   string // "" | "dm" | "group"
	GroupJID string // restrict to members of this group
	Q        string // free-text name search
}

// dmExistsExpr correlates an identity to a DM chat on the session: a direct chat
// whose peer JID is the identity's LID or its phone JID. One `?` (session_id).
const dmExistsExpr = `EXISTS (SELECT 1 FROM chats ch
	WHERE ch.session_id = ? AND ch.type = 'dm'
	  AND (ch.chat_jid = i.lid OR (i.phone_jid IS NOT NULL AND ch.chat_jid = i.phone_jid)))`

// groupExistsExpr correlates an identity to any group membership on the session.
// One `?` (session_id).
const groupExistsExpr = `EXISTS (SELECT 1 FROM whatsapp_group_members gm
	WHERE gm.session_id = ? AND gm.lid = i.lid)`

// List returns a page of found-user contacts for a session, applying ContactFilter
// and cursor pagination over the identity id. The DM/group EXISTS clauses both
// label each row's `source` AND scope the result to people THIS session saw.
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
	sb.WriteString("SELECT i.id, i.lid, i.phone_number, i.name, i.business_name, ")
	sb.WriteString(dmExistsExpr + " AS in_dm, ")
	args = append(args, sessionID)
	sb.WriteString(groupExistsExpr + " AS in_group ")
	args = append(args, sessionID)
	sb.WriteString("FROM whatsapp_identities i WHERE i.id > ? ")
	args = append(args, afterID)

	switch {
	case f.GroupJID != "":
		sb.WriteString(`AND EXISTS (SELECT 1 FROM whatsapp_group_members gm
			WHERE gm.session_id = ? AND gm.lid = i.lid AND gm.group_jid = ?) `)
		args = append(args, sessionID, f.GroupJID)
	case f.Source == "dm":
		sb.WriteString("AND " + dmExistsExpr + " ")
		args = append(args, sessionID)
	case f.Source == "group":
		sb.WriteString("AND " + groupExistsExpr + " ")
		args = append(args, sessionID)
	default:
		// "Anywhere": found in a DM or a group on this session.
		sb.WriteString("AND (" + dmExistsExpr + " OR " + groupExistsExpr + ") ")
		args = append(args, sessionID, sessionID)
	}

	if f.Q != "" {
		sb.WriteString("AND i.name LIKE ? ")
		args = append(args, "%"+f.Q+"%")
	}

	sb.WriteString("ORDER BY i.id ASC LIMIT ?")
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return Page[domain.Contact]{}, fmt.Errorf("store: list contacts: %w", err)
	}
	defer rows.Close()
	var out []domain.Contact
	for rows.Next() {
		var (
			c               domain.Contact
			inDM, inGroup   bool
		)
		if err := rows.Scan(&c.ID, &c.LID, &c.PhoneNumber, &c.Name, &c.BusinessName, &inDM, &inGroup); err != nil {
			return Page[domain.Contact]{}, err
		}
		// A direct relationship is the stronger signal; otherwise it's a group find.
		c.Source = "group"
		if inDM {
			c.Source = "dm"
		}
		_ = inGroup
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.Contact]{}, err
	}
	return pageFrom(out, limit, func(c domain.Contact) uint64 { return c.ID }), nil
}

// SeenInDM reports whether the session has a direct chat with the identity,
// matched by its LID or (when known) phone JID. Powers the GET /contacts/{lid}
// detail's `dm` flag.
func (r *ContactRepo) SeenInDM(ctx context.Context, sessionID, lid, phoneJID string) (bool, error) {
	const q = `SELECT EXISTS (SELECT 1 FROM chats
		WHERE session_id = ? AND type = 'dm'
		  AND (chat_jid = ? OR (? <> '' AND chat_jid = ?)))`
	var found bool
	if err := r.db.QueryRowContext(ctx, q, sessionID, lid, phoneJID, phoneJID).Scan(&found); err != nil {
		return false, fmt.Errorf("store: contact dm check: %w", err)
	}
	return found, nil
}
