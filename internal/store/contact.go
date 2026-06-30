package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// ContactRepo backs the "found users" feature as a PROJECTION over the central
// identity table — there is no contacts table. A person is "found" by a session
// when they appear in that session's chats as a DM, or in its group memberships
// (§5/§7). Identities are global; the per-session DM/group signals scope the view.
type ContactRepo struct {
	q *storedb.Queries
}

// NewContactRepo constructs a ContactRepo.
func NewContactRepo(db storedb.DBTX) *ContactRepo { return &ContactRepo{q: storedb.New(db)} }

// ContactFilter is the §11 GET /contacts filter set. Source "dm" restricts to
// people seen in a direct chat; "group" (or GroupJID) restricts to group members;
// Q is a substring match against the resolved push name in whatsapp_identities.
type ContactFilter struct {
	Source   string // "" | "dm" | "group"
	GroupJID string // restrict to members of this group
	Q        string // free-text name search
}

// List returns a page of found-user contacts for a session, applying ContactFilter
// and cursor pagination over the identity id. The DM/group EXISTS clauses both
// label each row's `source` AND scope the result to people THIS session saw.
func (r *ContactRepo) List(ctx context.Context, sessionID string, f ContactFilter, cursor string, limit int) (Page[domain.Contact], error) {
	afterID, err := parseCursor(cursor)
	if err != nil {
		return Page[domain.Contact]{}, err
	}
	limit = normLimit(limit)
	limit32 := int32(limit)
	namePattern := sqlString("%" + f.Q + "%")

	var out []domain.Contact
	switch {
	case f.GroupJID != "" && f.Q != "":
		rows, err := r.q.ListContactsByGroupSearch(ctx, storedb.ListContactsByGroupSearchParams{
			SessionID: sessionID, AfterID: afterID, GroupJid: f.GroupJID, NamePattern: namePattern, Limit: limit32,
		})
		if err != nil {
			return Page[domain.Contact]{}, fmt.Errorf("store: list contacts: %w", err)
		}
		out = make([]domain.Contact, 0, len(rows))
		for _, row := range rows {
			out = append(out, contactFromParts(row.ID, row.Lid, row.PhoneNumber, row.Name, row.BusinessName, row.InDm))
		}
	case f.GroupJID != "":
		rows, err := r.q.ListContactsByGroup(ctx, storedb.ListContactsByGroupParams{
			SessionID: sessionID, AfterID: afterID, GroupJid: f.GroupJID, Limit: limit32,
		})
		if err != nil {
			return Page[domain.Contact]{}, fmt.Errorf("store: list contacts: %w", err)
		}
		out = make([]domain.Contact, 0, len(rows))
		for _, row := range rows {
			out = append(out, contactFromParts(row.ID, row.Lid, row.PhoneNumber, row.Name, row.BusinessName, row.InDm))
		}
	case f.Source == "dm" && f.Q != "":
		rows, err := r.q.ListContactsDMSearch(ctx, storedb.ListContactsDMSearchParams{
			SessionID: sessionID, AfterID: afterID, NamePattern: namePattern, Limit: limit32,
		})
		if err != nil {
			return Page[domain.Contact]{}, fmt.Errorf("store: list contacts: %w", err)
		}
		out = make([]domain.Contact, 0, len(rows))
		for _, row := range rows {
			out = append(out, contactFromParts(row.ID, row.Lid, row.PhoneNumber, row.Name, row.BusinessName, row.InDm != 0))
		}
	case f.Source == "dm":
		rows, err := r.q.ListContactsDM(ctx, storedb.ListContactsDMParams{SessionID: sessionID, AfterID: afterID, Limit: limit32})
		if err != nil {
			return Page[domain.Contact]{}, fmt.Errorf("store: list contacts: %w", err)
		}
		out = make([]domain.Contact, 0, len(rows))
		for _, row := range rows {
			out = append(out, contactFromParts(row.ID, row.Lid, row.PhoneNumber, row.Name, row.BusinessName, row.InDm != 0))
		}
	case f.Source == "group" && f.Q != "":
		rows, err := r.q.ListContactsGroupSearch(ctx, storedb.ListContactsGroupSearchParams{
			SessionID: sessionID, AfterID: afterID, NamePattern: namePattern, Limit: limit32,
		})
		if err != nil {
			return Page[domain.Contact]{}, fmt.Errorf("store: list contacts: %w", err)
		}
		out = make([]domain.Contact, 0, len(rows))
		for _, row := range rows {
			out = append(out, contactFromParts(row.ID, row.Lid, row.PhoneNumber, row.Name, row.BusinessName, row.InDm))
		}
	case f.Source == "group":
		rows, err := r.q.ListContactsGroup(ctx, storedb.ListContactsGroupParams{SessionID: sessionID, AfterID: afterID, Limit: limit32})
		if err != nil {
			return Page[domain.Contact]{}, fmt.Errorf("store: list contacts: %w", err)
		}
		out = make([]domain.Contact, 0, len(rows))
		for _, row := range rows {
			out = append(out, contactFromParts(row.ID, row.Lid, row.PhoneNumber, row.Name, row.BusinessName, row.InDm))
		}
	case f.Q != "":
		rows, err := r.q.ListContactsAnywhereSearch(ctx, storedb.ListContactsAnywhereSearchParams{
			SessionID: sessionID, AfterID: afterID, NamePattern: namePattern, Limit: limit32,
		})
		if err != nil {
			return Page[domain.Contact]{}, fmt.Errorf("store: list contacts: %w", err)
		}
		out = make([]domain.Contact, 0, len(rows))
		for _, row := range rows {
			out = append(out, contactFromParts(row.ID, row.Lid, row.PhoneNumber, row.Name, row.BusinessName, row.InDm))
		}
	default:
		rows, err := r.q.ListContactsAnywhere(ctx, storedb.ListContactsAnywhereParams{SessionID: sessionID, AfterID: afterID, Limit: limit32})
		if err != nil {
			return Page[domain.Contact]{}, fmt.Errorf("store: list contacts: %w", err)
		}
		out = make([]domain.Contact, 0, len(rows))
		for _, row := range rows {
			out = append(out, contactFromParts(row.ID, row.Lid, row.PhoneNumber, row.Name, row.BusinessName, row.InDm))
		}
	}
	return pageFrom(out, limit, func(c domain.Contact) uint64 { return c.ID }), nil
}

func contactFromParts(id uint64, lid string, phoneNumber, name, businessName sql.NullString, inDM bool) domain.Contact {
	c := domain.Contact{
		ID:           id,
		LID:          lid,
		PhoneNumber:  stringPtrFromNull(phoneNumber),
		Name:         stringPtrFromNull(name),
		BusinessName: stringPtrFromNull(businessName),
		Source:       "group",
	}
	if inDM {
		c.Source = "dm"
	}
	return c
}

// SeenInDM reports whether the session has a direct chat with the identity,
// matched by its LID or (when known) phone JID. Powers the GET /contacts/{lid}
// detail's `dm` flag.
func (r *ContactRepo) SeenInDM(ctx context.Context, sessionID, lid, phoneJID string) (bool, error) {
	found, err := r.q.ContactSeenInDM(ctx, storedb.ContactSeenInDMParams{
		SessionID: sessionID,
		Lid:       lid,
		PhoneJid:  phoneJID,
	})
	if err != nil {
		return false, fmt.Errorf("store: contact dm check: %w", err)
	}
	return found, nil
}
