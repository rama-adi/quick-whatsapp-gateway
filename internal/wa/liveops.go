package wa

import (
	"context"
	"encoding/hex"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// This file exposes a manager-backed adapter for the "live ops" the resource
// services need (group management, on-WhatsApp checks, profile picture/about,
// presence, channels). The adapter resolves the per-session *whatsmeow.Client
// and translates between the gateway's string-JID API surface and whatsmeow's
// typed calls (recon §8/§9).
//
// The service package defines the port interfaces (consumer-defines convention);
// this adapter satisfies them structurally — there is no import of the service
// package here, so there is no cycle. The exported helper value types mirror the
// service ones field-for-field.

// liveClient is the slice of *whatsmeow.Client the live-ops adapter drives. The
// real client satisfies it; it is intentionally separate from waClient so the
// lifecycle fake in tests does not need to implement these methods.
type liveClient interface {
	GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error)
	GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
	CreateGroup(ctx context.Context, req whatsmeow.ReqCreateGroup) (*types.GroupInfo, error)
	UpdateGroupParticipants(ctx context.Context, jid types.JID, participants []types.JID, action whatsmeow.ParticipantChange) ([]types.GroupParticipant, error)
	SetGroupName(ctx context.Context, jid types.JID, name string) error
	SetGroupTopic(ctx context.Context, jid types.JID, previousID, newID, topic string) error
	SetGroupAnnounce(ctx context.Context, jid types.JID, announce bool) error
	SetGroupLocked(ctx context.Context, jid types.JID, locked bool) error
	GetGroupInviteLink(ctx context.Context, jid types.JID, reset bool) (string, error)
	JoinGroupWithLink(ctx context.Context, code string) (types.JID, error)
	LeaveGroup(ctx context.Context, jid types.JID) error
	IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error)
	GetProfilePictureInfo(ctx context.Context, jid types.JID, params *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error)
	GetUserInfo(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error)
	UpdateBlocklist(ctx context.Context, jid types.JID, action events.BlocklistChangeAction) (*types.Blocklist, error)
	SendPresence(ctx context.Context, state types.Presence) error
	SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error
	SubscribePresence(ctx context.Context, jid types.JID) error
	MarkRead(ctx context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID, receiptTypeExtra ...types.ReceiptType) error
}

// LiveOps returns a session-resolving adapter over the manager. It satisfies the
// service package's GroupOps / ContactDirectory / PresenceController / ChannelOps
// ports structurally. Wire it into the resource services in the composition root.
func (m *Manager) LiveOps() *LiveOps { return &LiveOps{m: m} }

// LiveOps adapts the manager's per-session whatsmeow clients to the resource
// services' live-ops ports.
type LiveOps struct{ m *Manager }

// client resolves the connected live client for a session, or an error mapped to
// the API not_implemented/unavailable envelope when the session is not connected.
func (l *LiveOps) client(id string) (liveClient, error) {
	ms := l.m.Get(id)
	if ms == nil {
		return nil, domain.ErrNotFound("session not found")
	}
	ms.mu.Lock()
	c := ms.client
	ms.mu.Unlock()
	if c == nil {
		return nil, domain.ErrNotImplemented("live WhatsApp client is not available for this session")
	}
	lc, ok := c.(liveClient)
	if !ok {
		return nil, domain.ErrNotImplemented("live WhatsApp client is not available for this session")
	}
	return lc, nil
}

// DecryptPollVote decrypts an incoming poll-vote (PollUpdateMessage) event and
// returns the SHA-256 hashes of the options the voter selected, hex-encoded.
// WhatsApp never sends the option text in a vote — only these hashes — so the
// caller resolves them against the originating poll's stored options. evt is the
// raw *events.Message the inbound pipeline received; a non-message or a session
// without a live client yields a not_implemented error.
func (l *LiveOps) DecryptPollVote(ctx context.Context, sessionID string, evt any) ([]string, error) {
	msg, ok := evt.(*events.Message)
	if !ok {
		return nil, domain.ErrValidation("not a message event")
	}
	c, _ := l.rawClient(sessionID)
	if c == nil {
		return nil, domain.ErrNotImplemented("live WhatsApp client is not available for this session")
	}
	vote, err := c.DecryptPollVote(ctx, msg)
	if err != nil {
		return nil, err
	}
	hashes := vote.GetSelectedOptions()
	out := make([]string, 0, len(hashes))
	for _, h := range hashes {
		out = append(out, hex.EncodeToString(h))
	}
	return out, nil
}

// ---- BackfillSource ----

// BackfillSessionData pulls data that whatsmeow exposes through direct APIs.
// Historical messages are handled by WhatsApp HistorySync events, not by a
// generic "fetch all messages" API.
//
// It is BEST-EFFORT: contacts and groups are fetched independently and a failure
// in one does not discard the other. The call only errors when BOTH sources fail
// (or the session isn't live), so a flaky single API can't nuke the whole job.
func (l *LiveOps) BackfillSessionData(ctx context.Context, sessionID string) (domain.BackfillSnapshot, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return domain.BackfillSnapshot{}, err
	}
	wmc, dev := l.rawClient(sessionID)

	// The whatsmeow contact store is the ONLY source of member push names —
	// WhatsApp does not include them in group metadata. It accumulates them from
	// message traffic / app-state sync (PutPushName). Fetch it once and reuse it
	// both for contact identities and to resolve each group member's push name.
	var (
		allContacts map[types.JID]types.ContactInfo
		cErr        error
	)
	if wmc != nil && wmc.Store != nil && wmc.Store.Contacts != nil {
		allContacts, cErr = wmc.Store.Contacts.GetAllContacts(ctx)
	} else {
		cErr = domain.ErrNotImplemented("live WhatsApp contact store is not available for this session")
	}
	if cErr != nil {
		l.m.log.WarnContext(ctx, "backfill contacts failed", "session", sessionID, "err", cErr)
	}
	names := buildNameIndex(allContacts)
	contacts := l.backfillContacts(ctx, dev, allContacts)

	var groups []domain.BackfillGroup
	joined, gErr := c.GetJoinedGroups(ctx)
	if gErr != nil {
		l.m.log.WarnContext(ctx, "backfill groups failed", "session", sessionID, "err", gErr)
	} else {
		groups = l.backfillGroups(ctx, dev, names, joined)
	}

	// Only a total wash is an error; partial data is still worth persisting.
	if cErr != nil && gErr != nil {
		return domain.BackfillSnapshot{}, gErr
	}
	return domain.BackfillSnapshot{Contacts: contacts, Groups: groups}, nil
}

// rawClient returns the concrete *whatsmeow.Client and its Device store for a
// session, or (nil, nil) when not live. Used for the store-backed reads
// (contact store, LID↔phone mapping) the narrow liveClient interface omits.
func (l *LiveOps) rawClient(sessionID string) (*whatsmeow.Client, *store.Device) {
	ms := l.m.Get(sessionID)
	if ms == nil {
		return nil, nil
	}
	ms.mu.Lock()
	raw := ms.client
	ms.mu.Unlock()
	c, ok := raw.(*whatsmeow.Client)
	if !ok || c == nil {
		return nil, nil
	}
	return c, c.Store
}

// OwnIDs returns the session's own canonical phone JID and LID, when available.
func (l *LiveOps) OwnIDs(_ context.Context, sessionID string) (string, string) {
	_, dev := l.rawClient(sessionID)
	if dev == nil {
		return "", ""
	}
	jid, lid := dev.GetJID(), dev.GetLID()
	var jidStr, lidStr string
	if !jid.IsEmpty() {
		jidStr = jid.ToNonAD().String()
	}
	if !lid.IsEmpty() {
		lidStr = lid.ToNonAD().String()
	}
	return jidStr, lidStr
}

func (l *LiveOps) backfillContacts(ctx context.Context, dev *store.Device, contacts map[types.JID]types.ContactInfo) []domain.BackfillContact {
	out := make([]domain.BackfillContact, 0, len(contacts))
	for jid, info := range contacts {
		lid, phoneJID := resolveLIDAndPhone(ctx, dev, jid)
		if lid == "" {
			// No LID mapping — can't key this identity consistently; skip it. Real
			// participants are still captured via group backfill + message capture.
			continue
		}
		out = append(out, domain.BackfillContact{
			LID:          lid,
			PhoneJID:     phoneJID,
			PhoneNumber:  phoneNumberOf(phoneJID),
			Name:         contactName(info),
			BusinessName: info.BusinessName,
		})
	}
	return out
}

// buildNameIndex maps canonical LID and phone-JID strings to the best display
// name from the cached contact store, so group members can be labeled by their
// push name. Entries without a usable name are skipped.
func buildNameIndex(contacts map[types.JID]types.ContactInfo) map[string]string {
	idx := make(map[string]string, len(contacts))
	for jid, info := range contacts {
		name := contactName(info)
		if name == "" {
			continue
		}
		switch jid.Server {
		case types.HiddenUserServer:
			idx[jid.ToNonAD().String()] = name
		case types.DefaultUserServer:
			idx[jid.ToNonAD().String()] = name
		}
	}
	return idx
}

func (l *LiveOps) backfillGroups(ctx context.Context, dev *store.Device, names map[string]string, groups []*types.GroupInfo) []domain.BackfillGroup {
	out := make([]domain.BackfillGroup, 0, len(groups))
	for _, g := range groups {
		if g == nil || g.JID.IsEmpty() {
			continue
		}
		members := make([]domain.BackfillMember, 0, len(g.Participants))
		for _, p := range g.Participants {
			m, ok := backfillMember(ctx, dev, names, p)
			if !ok {
				continue
			}
			members = append(members, m)
		}
		out = append(out, domain.BackfillGroup{
			GroupJID:     g.JID.String(),
			Subject:      g.Name,
			Description:  g.Topic,
			OwnerJID:     canonicalLID(g.OwnerJID),
			Participants: len(g.Participants),
			IsAnnounce:   g.IsAnnounce,
			IsLocked:     g.IsLocked,
			CreatedAtWA:  g.GroupCreated.UnixMilli(),
			Members:      members,
		})
	}
	return out
}

// backfillMember projects a whatsmeow GroupParticipant into a BackfillMember,
// resolving a canonical LID and the participant's phone. ok=false when the
// participant carries no usable LID.
func backfillMember(ctx context.Context, dev *store.Device, names map[string]string, p types.GroupParticipant) (domain.BackfillMember, bool) {
	lid := canonicalLID(p.LID)
	phoneJID := p.PhoneNumber
	if phoneJID.IsEmpty() && p.JID.Server == types.DefaultUserServer {
		phoneJID = p.JID
	}
	if lid == "" {
		// Participant addressed by phone only: try to map it to a LID so the
		// member + identity stay LID-keyed like everything else.
		if !phoneJID.IsEmpty() && dev != nil {
			if mapped, err := dev.LIDs.GetLIDForPN(ctx, phoneJID); err == nil {
				lid = canonicalLID(mapped)
			}
		}
	}
	if lid == "" {
		return domain.BackfillMember{}, false
	}
	role := domain.RoleMember
	if p.IsSuperAdmin {
		role = domain.RoleSuperAdmin
	} else if p.IsAdmin {
		role = domain.RoleAdmin
	}
	var phoneJIDStr string
	if !phoneJID.IsEmpty() {
		phoneJIDStr = phoneJID.ToNonAD().String()
	}
	// Resolve the member's push name from the contact-store index, by LID then by
	// phone. Empty when WhatsApp has never surfaced a name for them — it then
	// fills in later from message push-name capture.
	name := names[lid]
	if name == "" && phoneJIDStr != "" {
		name = names[phoneJIDStr]
	}
	return domain.BackfillMember{
		LID:         lid,
		JID:         phoneJIDStr,
		PhoneNumber: phoneNumberOf(phoneJIDStr),
		// Tag is the per-group label WhatsApp shows (often an obfuscated phone for
		// anonymous members) — a per-group identity, kept as-is on the pivot.
		Tag:  p.DisplayName,
		Name: name,
		Role: role,
	}, true
}

// resolveLIDAndPhone maps a contact-store key (which may be a phone JID or a LID,
// possibly device-suffixed) to a canonical LID string and a phone JID string.
// Either may be "" when the mapping is unknown.
func resolveLIDAndPhone(ctx context.Context, dev *store.Device, jid types.JID) (lid string, phoneJID string) {
	switch jid.Server {
	case types.HiddenUserServer: // already a LID
		lid = canonicalLID(jid)
		if dev != nil {
			if pn, err := dev.LIDs.GetPNForLID(ctx, jid.ToNonAD()); err == nil && !pn.IsEmpty() {
				phoneJID = pn.ToNonAD().String()
			}
		}
	case types.DefaultUserServer: // a phone JID
		phoneJID = jid.ToNonAD().String()
		if dev != nil {
			if l, err := dev.LIDs.GetLIDForPN(ctx, jid); err == nil {
				lid = canonicalLID(l)
			}
		}
	}
	return lid, phoneJID
}

// canonicalLID renders a JID as a canonical (non-AD) LID string, or "" when the
// JID is not a LID. Stripping the agent/device part (":NN") collapses the
// per-device duplicates that made the identities table inconsistent.
func canonicalLID(j types.JID) string {
	if j.IsEmpty() || j.Server != types.HiddenUserServer {
		return ""
	}
	return j.ToNonAD().String()
}

// phoneNumberOf extracts the bare phone number from a "<num>@s.whatsapp.net" JID
// string, or "" for anything else.
func phoneNumberOf(jidStr string) string {
	const suffix = "@" + types.DefaultUserServer
	if jidStr == "" || len(jidStr) <= len(suffix) || jidStr[len(jidStr)-len(suffix):] != suffix {
		return ""
	}
	return jidStr[:len(jidStr)-len(suffix)]
}

// contactName picks the best display name from a ContactInfo, preferring the push
// name. Never returns an obfuscated/redacted value.
func contactName(info types.ContactInfo) string {
	switch {
	case info.PushName != "":
		return info.PushName
	case info.FullName != "":
		return info.FullName
	case info.FirstName != "":
		return info.FirstName
	default:
		return ""
	}
}

func parseJID(s string) (types.JID, error) {
	jid, err := types.ParseJID(s)
	if err != nil {
		return types.JID{}, domain.ErrValidation("invalid jid: " + s)
	}
	return jid, nil
}

func parseJIDs(ss []string) ([]types.JID, error) {
	out := make([]types.JID, 0, len(ss))
	for _, s := range ss {
		j, err := parseJID(s)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// ---- GroupOps ----

func toGroupInfo(g *types.GroupInfo) domain.GroupInfo {
	if g == nil {
		return domain.GroupInfo{}
	}
	return domain.GroupInfo{
		GroupJID:     g.JID.String(),
		Subject:      g.Name,
		Description:  g.Topic,
		OwnerJID:     g.OwnerJID.String(),
		Participants: len(g.Participants),
		IsAnnounce:   g.IsAnnounce,
		IsLocked:     g.IsLocked,
	}
}

// CreateGroup creates a group.
func (l *LiveOps) CreateGroup(ctx context.Context, sessionID, name string, participants []string) (domain.GroupInfo, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	jids, err := parseJIDs(participants)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	g, err := c.CreateGroup(ctx, whatsmeow.ReqCreateGroup{Name: name, Participants: jids})
	if err != nil {
		return domain.GroupInfo{}, err
	}
	return toGroupInfo(g), nil
}

// GetGroupInfo fetches live group metadata.
func (l *LiveOps) GetGroupInfo(ctx context.Context, sessionID, groupJID string) (domain.GroupInfo, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	jid, err := parseJID(groupJID)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	g, err := c.GetGroupInfo(ctx, jid)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	return toGroupInfo(g), nil
}

// UpdateParticipants applies an add/remove/promote/demote.
func (l *LiveOps) UpdateParticipants(ctx context.Context, sessionID, groupJID string, participants []string, action domain.GroupParticipantAction) error {
	c, err := l.client(sessionID)
	if err != nil {
		return err
	}
	jid, err := parseJID(groupJID)
	if err != nil {
		return err
	}
	jids, err := parseJIDs(participants)
	if err != nil {
		return err
	}
	var pc whatsmeow.ParticipantChange
	switch action {
	case domain.GroupActionAdd:
		pc = whatsmeow.ParticipantChangeAdd
	case domain.GroupActionRemove:
		pc = whatsmeow.ParticipantChangeRemove
	case domain.GroupActionPromote:
		pc = whatsmeow.ParticipantChangePromote
	case domain.GroupActionDemote:
		pc = whatsmeow.ParticipantChangeDemote
	default:
		return domain.ErrValidation("invalid participant action")
	}
	_, err = c.UpdateGroupParticipants(ctx, jid, jids, pc)
	return err
}

// UpdateSettings applies subject/description/announce/locked.
func (l *LiveOps) UpdateSettings(ctx context.Context, sessionID, groupJID string, s domain.GroupSettings) error {
	c, err := l.client(sessionID)
	if err != nil {
		return err
	}
	jid, err := parseJID(groupJID)
	if err != nil {
		return err
	}
	if s.Subject != nil {
		if err := c.SetGroupName(ctx, jid, *s.Subject); err != nil {
			return err
		}
	}
	if s.Description != nil {
		if err := c.SetGroupTopic(ctx, jid, "", "", *s.Description); err != nil {
			return err
		}
	}
	if s.Announce != nil {
		if err := c.SetGroupAnnounce(ctx, jid, *s.Announce); err != nil {
			return err
		}
	}
	if s.Locked != nil {
		if err := c.SetGroupLocked(ctx, jid, *s.Locked); err != nil {
			return err
		}
	}
	return nil
}

// GetInviteLink returns the group invite link (reset=true revokes+regenerates).
func (l *LiveOps) GetInviteLink(ctx context.Context, sessionID, groupJID string, reset bool) (string, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return "", err
	}
	jid, err := parseJID(groupJID)
	if err != nil {
		return "", err
	}
	return c.GetGroupInviteLink(ctx, jid, reset)
}

// JoinWithLink joins a group from an invite code/link.
func (l *LiveOps) JoinWithLink(ctx context.Context, sessionID, code string) (string, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return "", err
	}
	jid, err := c.JoinGroupWithLink(ctx, code)
	if err != nil {
		return "", err
	}
	return jid.String(), nil
}

// Leave leaves a group.
func (l *LiveOps) Leave(ctx context.Context, sessionID, groupJID string) error {
	c, err := l.client(sessionID)
	if err != nil {
		return err
	}
	jid, err := parseJID(groupJID)
	if err != nil {
		return err
	}
	return c.LeaveGroup(ctx, jid)
}

// ---- ContactDirectory ----

// IsOnWhatsApp checks phone numbers.
func (l *LiveOps) IsOnWhatsApp(ctx context.Context, sessionID string, phones []string) ([]domain.OnWhatsApp, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return nil, err
	}
	res, err := c.IsOnWhatsApp(ctx, phones)
	if err != nil {
		return nil, err
	}
	out := make([]domain.OnWhatsApp, 0, len(res))
	for _, r := range res {
		out = append(out, domain.OnWhatsApp{Query: r.Query, JID: r.JID.String(), IsIn: r.IsIn})
	}
	return out, nil
}

// ProfilePicture returns a contact's profile picture (nil-safe: WhatsApp returns
// (nil,nil) when hidden — mapped to an empty result).
func (l *LiveOps) ProfilePicture(ctx context.Context, sessionID, jid string) (domain.ProfilePicture, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return domain.ProfilePicture{}, err
	}
	j, err := parseJID(jid)
	if err != nil {
		return domain.ProfilePicture{}, err
	}
	info, err := c.GetProfilePictureInfo(ctx, j, nil)
	if err != nil {
		return domain.ProfilePicture{}, err
	}
	if info == nil {
		return domain.ProfilePicture{}, nil
	}
	return domain.ProfilePicture{URL: info.URL, ID: info.ID}, nil
}

// About returns a contact's status text.
func (l *LiveOps) About(ctx context.Context, sessionID, jid string) (string, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return "", err
	}
	j, err := parseJID(jid)
	if err != nil {
		return "", err
	}
	info, err := c.GetUserInfo(ctx, []types.JID{j})
	if err != nil {
		return "", err
	}
	if ui, ok := info[j]; ok {
		return ui.Status, nil
	}
	return "", nil
}

// SetBlocked blocks/unblocks a contact.
func (l *LiveOps) SetBlocked(ctx context.Context, sessionID, jid string, blocked bool) error {
	c, err := l.client(sessionID)
	if err != nil {
		return err
	}
	j, err := parseJID(jid)
	if err != nil {
		return err
	}
	action := events.BlocklistChangeActionUnblock
	if blocked {
		action = events.BlocklistChangeActionBlock
	}
	_, err = c.UpdateBlocklist(ctx, j, action)
	return err
}

// ---- PresenceController ----

// SetPresence sets account-wide presence.
func (l *LiveOps) SetPresence(ctx context.Context, sessionID, state string) error {
	c, err := l.client(sessionID)
	if err != nil {
		return err
	}
	p := types.PresenceUnavailable
	if state == "online" {
		p = types.PresenceAvailable
	}
	return c.SendPresence(ctx, p)
}

// SetChatPresence sets per-chat typing state.
func (l *LiveOps) SetChatPresence(ctx context.Context, sessionID, chatJID, state string) error {
	c, err := l.client(sessionID)
	if err != nil {
		return err
	}
	j, err := parseJID(chatJID)
	if err != nil {
		return err
	}
	var (
		cp    types.ChatPresence
		media types.ChatPresenceMedia
	)
	switch state {
	case "composing":
		cp = types.ChatPresenceComposing
	case "paused":
		cp = types.ChatPresencePaused
	case "recording":
		cp = types.ChatPresenceComposing
		media = types.ChatPresenceMediaAudio
	default:
		return domain.ErrValidation("invalid chat presence state")
	}
	return c.SendChatPresence(ctx, j, cp, media)
}

// GetPresence subscribes to WhatsApp presence updates for a contact. WhatsApp
// does not expose a synchronous "current presence" read; the subscription causes
// subsequent *events.Presence frames to arrive through the normal inbound event
// handler. The REST response is therefore an explicit unknown snapshot.
func (l *LiveOps) GetPresence(ctx context.Context, sessionID, chatJID string) (domain.PresenceStatus, error) {
	c, err := l.client(sessionID)
	if err != nil {
		return domain.PresenceStatus{}, err
	}
	j, err := parseJID(chatJID)
	if err != nil {
		return domain.PresenceStatus{}, err
	}
	if err := c.SubscribePresence(ctx, j); err != nil {
		return domain.PresenceStatus{}, err
	}
	return domain.PresenceStatus{
		ChatJID: chatJID,
		From:    j.String(),
		State:   "unknown",
	}, nil
}

// SendReadReceipt marks one or more incoming messages as read.
func (l *LiveOps) SendReadReceipt(ctx context.Context, sessionID, chatJID, senderJID string, messageIDs []string) error {
	c, err := l.client(sessionID)
	if err != nil {
		return err
	}
	chat, err := parseJID(chatJID)
	if err != nil {
		return err
	}
	var sender types.JID
	if senderJID != "" {
		sender, err = parseJID(senderJID)
		if err != nil {
			return err
		}
	}
	ids := make([]types.MessageID, 0, len(messageIDs))
	for _, id := range messageIDs {
		if id != "" {
			ids = append(ids, types.MessageID(id))
		}
	}
	return c.MarkRead(ctx, ids, time.Now(), chat, sender)
}

// SendPresence sends per-chat composing/paused presence for the inbound pipeline.
func (l *LiveOps) SendPresence(ctx context.Context, sessionID, chatJID, state string) error {
	return l.SetChatPresence(ctx, sessionID, chatJID, state)
}

// ---- ChannelOps ----
//
// whatsmeow's newsletter API surface (CreateNewsletter / FollowNewsletter /
// UnfollowNewsletter / NewsletterToggleMute) is not part of the narrow live
// client wired for v1; channels are reported as not_implemented consistently
// with the media send types.

// Create is not implemented in v1.
func (l *LiveOps) Create(ctx context.Context, sessionID, name, description string) (string, error) {
	return "", domain.ErrNotImplemented("channel create is not implemented yet")
}

// Follow is not implemented in v1.
func (l *LiveOps) Follow(ctx context.Context, sessionID, jid string) error {
	return domain.ErrNotImplemented("channel follow is not implemented yet")
}

// Unfollow is not implemented in v1.
func (l *LiveOps) Unfollow(ctx context.Context, sessionID, jid string) error {
	return domain.ErrNotImplemented("channel unfollow is not implemented yet")
}

// Mute is not implemented in v1.
func (l *LiveOps) Mute(ctx context.Context, sessionID, jid string, mute bool) error {
	return domain.ErrNotImplemented("channel mute is not implemented yet")
}

// ---- StatusPoster ----
//
// Status posting uses SendMessage to the status broadcast JID; that path goes
// through the outbound Sender (currently stubbed), so it is reported as
// not_implemented here until the live client is wired end-to-end.

// PostText is not implemented in v1.
func (l *LiveOps) PostText(ctx context.Context, sessionID, text string) (string, error) {
	return "", domain.ErrNotImplemented("status posting is not implemented yet")
}
