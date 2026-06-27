package service

import (
	"context"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// ContactService backs the "found users" feature (§11 Contacts). The list/detail
// views are served from the store (contacts + identities + group_members); the
// check/picture/about/block calls are delegated to the live ContactDirectory.
type ContactService struct {
	store     *store.Store
	directory ContactDirectory
	log       *slog.Logger
}

// NewContactService constructs a ContactService. directory may be nil (the live
// sub-resources then report the client as unavailable).
func NewContactService(s *store.Store, directory ContactDirectory, log *slog.Logger) *ContactService {
	if log == nil {
		log = slog.Default()
	}
	return &ContactService{store: s, directory: directory, log: log}
}

func (s *ContactService) requireSession(ctx context.Context, organizationID, sessionID string) error {
	sess, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.OrganizationID != organizationID {
		return domain.ErrNotFound("session not found")
	}
	return nil
}

// List returns a page of found-user contacts, applying the §11 filters
// (?source=dm|group, ?group=, ?q=).
func (s *ContactService) List(ctx context.Context, organizationID, sessionID string, f store.ContactFilter, cursor string, limit int) (store.Page[domain.Contact], error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return store.Page[domain.Contact]{}, err
	}
	if f.Source != "" && f.Source != "dm" && f.Source != "group" {
		return store.Page[domain.Contact]{}, domain.ErrValidation("source must be dm or group")
	}
	return s.store.Contacts.List(ctx, sessionID, f, cursor, limit)
}

// ContactGroup is one group membership in the §11 GET /contacts/{lid} detail
// view — the identity↔group pivot row, with the per-group member `tag`.
type ContactGroup struct {
	JID      string  `json:"jid"`
	Name     *string `json:"name,omitempty"`
	Tag      *string `json:"tag,omitempty"`
	Role     string  `json:"role"`
	LastSeen int64   `json:"lastSeen"`
}

// ContactDetail is the §11 GET /contacts/{lid} response: the central identity
// (push name preferred) + whether the session has a DM with them + their group
// memberships (with per-group tag).
type ContactDetail struct {
	Identity *domain.Identity `json:"identity,omitempty"`
	DM       bool             `json:"dm"`
	Groups   []ContactGroup   `json:"groups"`
}

// Get returns a contact's identity + DM flag + group memberships. The identity is
// the central record (push name preferred); the DM flag is derived from the
// session's chats; `tag` is per-group from the membership pivot.
func (s *ContactService) Get(ctx context.Context, organizationID, sessionID, lid string) (ContactDetail, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return ContactDetail{}, err
	}
	id, err := s.store.Identities.GetByLID(ctx, lid)
	if err != nil {
		return ContactDetail{}, err
	}
	detail := ContactDetail{Identity: &id, Groups: []ContactGroup{}}

	phoneJID := ""
	if id.PhoneJID != nil {
		phoneJID = *id.PhoneJID
	}
	dm, err := s.store.Contacts.SeenInDM(ctx, sessionID, lid, phoneJID)
	if err != nil {
		return ContactDetail{}, err
	}
	detail.DM = dm

	members, err := s.store.GroupMembers.ListByContact(ctx, sessionID, lid)
	if err != nil {
		return ContactDetail{}, err
	}
	for _, m := range members {
		cg := ContactGroup{
			JID:      m.GroupJID,
			Tag:      m.Tag,
			Role:     string(m.Role),
			LastSeen: m.LastSeenAt,
		}
		if g, err := s.store.Groups.GetByJID(ctx, m.GroupJID); err == nil {
			cg.Name = g.Subject
		}
		detail.Groups = append(detail.Groups, cg)
	}
	return detail, nil
}

// Check reports whether a phone number is on WhatsApp (§11 contacts/check).
func (s *ContactService) Check(ctx context.Context, organizationID, sessionID, phone string) (OnWhatsApp, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return OnWhatsApp{}, err
	}
	if phone == "" {
		return OnWhatsApp{}, domain.ErrValidation("phone is required")
	}
	if s.directory == nil {
		return OnWhatsApp{}, errLiveUnavailable()
	}
	res, err := s.directory.IsOnWhatsApp(ctx, sessionID, []string{phone})
	if err != nil {
		return OnWhatsApp{}, err
	}
	if len(res) == 0 {
		return OnWhatsApp{Query: phone}, nil
	}
	return res[0], nil
}

// Picture returns a contact's profile picture (§11 contacts/{jid}/picture).
func (s *ContactService) Picture(ctx context.Context, organizationID, sessionID, jid string) (ProfilePicture, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return ProfilePicture{}, err
	}
	if s.directory == nil {
		return ProfilePicture{}, errLiveUnavailable()
	}
	return s.directory.ProfilePicture(ctx, sessionID, jid)
}

// About returns a contact's status text (§11 contacts/{jid}/about).
func (s *ContactService) About(ctx context.Context, organizationID, sessionID, jid string) (string, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return "", err
	}
	if s.directory == nil {
		return "", errLiveUnavailable()
	}
	return s.directory.About(ctx, sessionID, jid)
}

// SetBlocked blocks or unblocks a contact (§11 contacts/{jid}/block|unblock).
func (s *ContactService) SetBlocked(ctx context.Context, organizationID, sessionID, jid string, blocked bool) error {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return err
	}
	if s.directory == nil {
		return errLiveUnavailable()
	}
	return s.directory.SetBlocked(ctx, sessionID, jid, blocked)
}
