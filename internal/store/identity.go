package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// IdentityRepo is the repository for whatsapp_identities — global LID→phone/name
// resolution, upserted by lid on every inbound capture (§7).
type IdentityRepo struct {
	db storedb.DBTX
	q  *storedb.Queries
}

// NewIdentityRepo constructs an IdentityRepo.
func NewIdentityRepo(db storedb.DBTX) *IdentityRepo { return &IdentityRepo{db: db, q: storedb.New(db)} }

func identityFromRow(row storedb.WhatsappIdentity) domain.Identity {
	return domain.Identity{
		ID:           row.ID,
		LID:          row.Lid,
		PhoneNumber:  stringPtrFromNull(row.PhoneNumber),
		PhoneJID:     stringPtrFromNull(row.PhoneJid),
		Name:         stringPtrFromNull(row.Name),
		BusinessName: stringPtrFromNull(row.BusinessName),
		FirstSeenAt:  row.FirstSeenAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

// Upsert inserts or updates an identity by lid. On conflict the resolvable
// fields refresh but only when the new value is non-NULL (COALESCE keeps a
// previously-known phone/name if a later sighting lacks it); first_seen_at is
// preserved. This is the §7 "prefer push name / fill in resolution" behavior.
func (r *IdentityRepo) Upsert(ctx context.Context, i domain.Identity) error {
	err := r.q.UpsertIdentity(ctx, storedb.UpsertIdentityParams{
		Lid:          i.LID,
		PhoneNumber:  nullString(i.PhoneNumber),
		PhoneJid:     nullString(i.PhoneJID),
		Name:         nullString(i.Name),
		BusinessName: nullString(i.BusinessName),
		FirstSeenAt:  i.FirstSeenAt,
		UpdatedAt:    i.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("store: upsert identity: %w", err)
	}
	if i.PhoneJID != nil {
		if err := mergeDMChatAlias(ctx, r.db, "", i.LID, *i.PhoneJID); err != nil {
			return err
		}
	}
	return nil
}

// FillNameByJID opportunistically fills an existing identity's display name from
// a push-name sighting (contact.update / push-name events carry only a JID, not a
// canonical LID). It matches the row by either its lid or its phone_jid and only
// writes when we don't already have a name — so a fresh sighting enriches a
// nameless identity but never clobbers a known name. It NEVER inserts: a contact
// the account merely synced shouldn't create an identity for someone never
// actually encountered. No match (zero rows) is not an error.
func (r *IdentityRepo) FillNameByJID(ctx context.Context, jid, name string, nowMs int64) error {
	if jid == "" || name == "" {
		return nil
	}
	err := r.q.FillIdentityNameByJID(ctx, storedb.FillIdentityNameByJIDParams{
		Name:      sqlString(name),
		UpdatedAt: nowMs,
		Lid:       jid,
		PhoneJid:  sqlString(jid),
	})
	if err != nil {
		return fmt.Errorf("store: fill identity name: %w", err)
	}
	return nil
}

// NamesForMentions resolves display names for a set of mention JIDs (each a
// "@lid" or "@s.whatsapp.net" JID as stored in messages.mentions). It returns a
// map keyed by each resolvable JID's USER-PART — the token that appears after "@"
// in a message body (e.g. "205227043110953") — to the identity's name, matching
// by lid or phone_jid. Mentions we can't resolve to a known, named identity are
// omitted; empty/all-unresolvable input returns a nil map. One query regardless
// of mention count.
func (r *IdentityRepo) NamesForMentions(ctx context.Context, jids []string) (map[string]string, error) {
	uniq := make([]string, 0, len(jids))
	seen := make(map[string]struct{}, len(jids))
	for _, j := range jids {
		if j == "" {
			continue
		}
		if _, ok := seen[j]; ok {
			continue
		}
		seen[j] = struct{}{}
		uniq = append(uniq, j)
	}
	if len(uniq) == 0 {
		return nil, nil
	}

	jidList := strings.Join(uniq, ",")
	rows, err := r.q.NamesForMentionJIDs(ctx, storedb.NamesForMentionJIDsParams{
		FINDINSET:   jidList,
		FINDINSET_2: jidList,
	})
	if err != nil {
		return nil, fmt.Errorf("store: resolve mention names: %w", err)
	}

	// Map every known id (lid and phone_jid) -> name, then re-key by the mention's
	// user-part so it lines up with the "@<userpart>" token in the body.
	byID := make(map[string]string)
	for _, row := range rows {
		name := stringPtrFromNull(row.Name)
		if name == nil {
			continue
		}
		byID[row.Lid] = *name
		phoneJID := stringPtrFromNull(row.PhoneJid)
		if phoneJID != nil && *phoneJID != "" {
			byID[*phoneJID] = *name
		}
	}

	out := make(map[string]string, len(uniq))
	for _, j := range uniq {
		if name, ok := byID[j]; ok {
			out[mentionUserPart(j)] = name
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// mentionUserPart returns the token before "@" (and before any ":device" suffix)
// in a JID — the form WhatsApp embeds as "@<userpart>" in a message body.
func mentionUserPart(jid string) string {
	if i := strings.IndexAny(jid, "@:"); i >= 0 {
		return jid[:i]
	}
	return jid
}

// GetByLID fetches an identity by its unique lid. Maps no-rows to not_found.
func (r *IdentityRepo) GetByLID(ctx context.Context, lid string) (domain.Identity, error) {
	row, err := r.q.GetIdentityByLID(ctx, storedb.GetIdentityByLIDParams{Lid: lid})
	if err != nil {
		return domain.Identity{}, notFound(err, "identity")
	}
	return identityFromRow(row), nil
}

func (r *IdentityRepo) GetByID(ctx context.Context, id uint64) (domain.Identity, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, lid, phone_number, phone_jid, name, business_name, first_seen_at, updated_at FROM whatsapp_identities WHERE id = ?`, id)
	var i domain.Identity
	var phoneNumber, phoneJID, name, businessName sql.NullString
	if err := row.Scan(&i.ID, &i.LID, &phoneNumber, &phoneJID, &name, &businessName, &i.FirstSeenAt, &i.UpdatedAt); err != nil {
		return domain.Identity{}, notFound(err, "identity")
	}
	i.PhoneNumber = stringPtrFromNull(phoneNumber)
	i.PhoneJID = stringPtrFromNull(phoneJID)
	i.Name = stringPtrFromNull(name)
	i.BusinessName = stringPtrFromNull(businessName)
	return i, nil
}
