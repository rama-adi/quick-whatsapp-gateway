package service

import (
	"context"
	"log/slog"
	"sync"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// ---------------------------------------------------------------------------
// ChannelService — channels/newsletters (§11 Channels).
// ---------------------------------------------------------------------------

// ChannelService backs the channel endpoints over the live ChannelOps surface.
// Channel message history (§11 GET /channels/{jid}/messages) is read from the
// stored messages by chat_jid.
type ChannelService struct {
	store *store.Store
	ops   ChannelOps
	log   *slog.Logger
}

// NewChannelService constructs a ChannelService.
func NewChannelService(s *store.Store, ops ChannelOps, log *slog.Logger) *ChannelService {
	if log == nil {
		log = slog.Default()
	}
	return &ChannelService{store: s, ops: ops, log: log}
}

func (s *ChannelService) requireSession(ctx context.Context, organizationID, sessionID string) error {
	sess, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.OrganizationID != organizationID {
		return domain.ErrNotFound("session not found")
	}
	return nil
}

func (s *ChannelService) live(ctx context.Context, organizationID, sessionID string) error {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return err
	}
	if s.ops == nil {
		return errLiveUnavailable()
	}
	return nil
}

// Create creates a channel/newsletter (§11 POST /channels).
func (s *ChannelService) Create(ctx context.Context, organizationID, sessionID, name, description string) (string, error) {
	if name == "" {
		return "", domain.ErrValidation("name is required")
	}
	if err := s.live(ctx, organizationID, sessionID); err != nil {
		return "", err
	}
	return s.ops.Create(ctx, sessionID, name, description)
}

// Follow follows a channel (§11 POST /channels/{jid}:follow).
func (s *ChannelService) Follow(ctx context.Context, organizationID, sessionID, jid string) error {
	if err := s.live(ctx, organizationID, sessionID); err != nil {
		return err
	}
	return s.ops.Follow(ctx, sessionID, jid)
}

// Unfollow unfollows a channel (§11 POST /channels/{jid}:unfollow).
func (s *ChannelService) Unfollow(ctx context.Context, organizationID, sessionID, jid string) error {
	if err := s.live(ctx, organizationID, sessionID); err != nil {
		return err
	}
	return s.ops.Unfollow(ctx, sessionID, jid)
}

// Mute mutes or unmutes a channel (§11 POST /channels/{jid}:mute).
func (s *ChannelService) Mute(ctx context.Context, organizationID, sessionID, jid string, mute bool) error {
	if err := s.live(ctx, organizationID, sessionID); err != nil {
		return err
	}
	return s.ops.Mute(ctx, sessionID, jid, mute)
}

// Messages returns stored channel messages (§11 GET /channels/{jid}/messages).
func (s *ChannelService) Messages(ctx context.Context, organizationID, sessionID, jid, cursor string, limit int) (store.Page[domain.Message], error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return store.Page[domain.Message]{}, err
	}
	return s.store.Messages.ListByChat(ctx, sessionID, jid, cursor, limit)
}

// ---------------------------------------------------------------------------
// StatusService — status/stories (§11 Status).
// ---------------------------------------------------------------------------

// StatusService backs the status (stories) endpoints.
type StatusService struct {
	store  *store.Store
	poster StatusPoster
	log    *slog.Logger
}

// NewStatusService constructs a StatusService.
func NewStatusService(s *store.Store, poster StatusPoster, log *slog.Logger) *StatusService {
	if log == nil {
		log = slog.Default()
	}
	return &StatusService{store: s, poster: poster, log: log}
}

func (s *StatusService) requireSession(ctx context.Context, organizationID, sessionID string) error {
	sess, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.OrganizationID != organizationID {
		return domain.ErrNotFound("session not found")
	}
	return nil
}

// StatusKind discriminates the §11 POST /status body. Only "text" is supported in
// v1; media kinds return not_implemented consistently with the media send types.
const (
	StatusKindText  = "text"
	StatusKindImage = "image"
)

// PostText posts a text status (§11 POST /status).
func (s *StatusService) PostText(ctx context.Context, organizationID, sessionID, text string) (string, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return "", err
	}
	if text == "" {
		return "", domain.ErrValidation("text is required")
	}
	if s.poster == nil {
		return "", errLiveUnavailable()
	}
	return s.poster.PostText(ctx, sessionID, text)
}

// PostImage is the image-status path — not implemented in v1 (§11: image => 501).
func (s *StatusService) PostImage(ctx context.Context, organizationID, sessionID string) (string, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return "", err
	}
	return "", domain.ErrNotImplemented("image status is not implemented yet")
}

// ---------------------------------------------------------------------------
// PresenceService — account-wide presence (§11 Presence).
// ---------------------------------------------------------------------------

// PresenceService backs the presence endpoint.
type PresenceService struct {
	store   *store.Store
	control PresenceController
	log     *slog.Logger
}

// NewPresenceService constructs a PresenceService.
func NewPresenceService(s *store.Store, control PresenceController, log *slog.Logger) *PresenceService {
	if log == nil {
		log = slog.Default()
	}
	return &PresenceService{store: s, control: control, log: log}
}

func (s *PresenceService) requireSession(ctx context.Context, organizationID, sessionID string) error {
	sess, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.OrganizationID != organizationID {
		return domain.ErrNotFound("session not found")
	}
	return nil
}

// Set sets account-wide presence: "online" or "offline" (§11 PUT /presence).
func (s *PresenceService) Set(ctx context.Context, organizationID, sessionID, state string) error {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return err
	}
	if state != "online" && state != "offline" {
		return domain.ErrValidation("state must be online or offline")
	}
	if s.control == nil {
		return errLiveUnavailable()
	}
	return s.control.SetPresence(ctx, sessionID, state)
}

// ---------------------------------------------------------------------------
// AdminService — cross-organization oversight (§11 Admin).
// ---------------------------------------------------------------------------

// AdminService backs the super-admin oversight endpoints.
type AdminService struct {
	store    *store.Store
	backfill BackfillSource
	log      *slog.Logger

	mu   sync.Mutex
	jobs map[string]*domain.BackfillJob
}

// NewAdminService constructs an AdminService.
func NewAdminService(s *store.Store, backfill BackfillSource, log *slog.Logger) *AdminService {
	if log == nil {
		log = slog.Default()
	}
	return &AdminService{store: s, backfill: backfill, log: log, jobs: make(map[string]*domain.BackfillJob)}
}

// ListAllSessions returns every session across all organizations (super_admin).
func (s *AdminService) ListAllSessions(ctx context.Context) ([]domain.WASession, error) {
	return s.store.Sessions.ListAll(ctx)
}

// StartBackfill starts one in-memory backfill job for a session.
func (s *AdminService) StartBackfill(ctx context.Context, sessionID string) (domain.BackfillJob, error) {
	sess, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return domain.BackfillJob{}, err
	}
	if s.backfill == nil {
		return domain.BackfillJob{}, errLiveUnavailable()
	}

	now := domain.NowMs()
	job := &domain.BackfillJob{
		ID:             domain.NewPrefixedID("bf_"),
		SessionID:      sess.ID,
		OrganizationID: sess.OrganizationID,
		Status:         "running",
		StartedAt:      now,
	}

	s.mu.Lock()
	if runningJob := s.jobs[sess.ID]; runningJob != nil && runningJob.Status == "running" {
		running := *runningJob
		s.mu.Unlock()
		return running, domain.ErrConflict("backfill already running for session")
	}
	s.jobs[sess.ID] = job
	s.mu.Unlock()

	go s.runBackfill(context.Background(), job.ID, sess.ID)
	return *job, nil
}

// BackfillStatus returns the current or most recent backfill job.
func (s *AdminService) BackfillStatus(_ context.Context, sessionID string) (domain.BackfillJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[sessionID]
	if job == nil {
		return domain.BackfillJob{}, domain.ErrNotFound("backfill job not found")
	}
	return *job, nil
}

func (s *AdminService) runBackfill(ctx context.Context, jobID, sessionID string) {
	snapshot, err := s.backfill.BackfillSessionData(ctx, sessionID)
	contacts, groups, members := 0, 0, 0
	if err == nil {
		contacts, groups, members, err = s.persistBackfill(ctx, sessionID, snapshot)
	}
	finished := domain.NowMs()
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[sessionID]
	if job == nil || job.ID != jobID {
		return
	}
	job.FinishedAt = &finished
	job.Contacts = contacts
	job.Groups = groups
	job.GroupMembers = members
	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
		s.log.WarnContext(ctx, "admin backfill failed", "session", sessionID, "job", jobID, "err", err)
		return
	}
	job.Status = "succeeded"
}

func (s *AdminService) persistBackfill(ctx context.Context, sessionID string, snapshot domain.BackfillSnapshot) (int, int, int, error) {
	now := domain.NowMs()
	contactCount := 0
	for _, c := range snapshot.Contacts {
		if c.LID == "" {
			continue
		}
		// name is left NULL when unknown so a real push name captured later wins
		// (Identity.Upsert COALESCEs) — never store the LID/JID as the name.
		// Contacts feed the central identity table; DM "found" status is derived
		// later from the chats table, so there is no per-session contact row. Name
		// is left NULL when unknown so a real push name captured later wins
		// (Identity.Upsert COALESCEs) — never store the LID/JID as the name.
		if err := s.store.Identities.Upsert(ctx, domain.Identity{
			LID:          c.LID,
			PhoneNumber:  stringPtr(c.PhoneNumber),
			PhoneJID:     stringPtr(c.PhoneJID),
			Name:         stringPtr(c.Name),
			BusinessName: stringPtr(c.BusinessName),
			FirstSeenAt:  now,
			UpdatedAt:    now,
		}); err != nil {
			return 0, 0, 0, err
		}
		contactCount++
	}
	groupCount := 0
	memberCount := 0
	for _, g := range snapshot.Groups {
		if g.GroupJID == "" {
			continue
		}
		participantCount := g.Participants
		isAnnounce := g.IsAnnounce
		isLocked := g.IsLocked
		var createdAtWA *int64
		if g.CreatedAtWA > 0 {
			createdAtWA = &g.CreatedAtWA
		}
		if err := s.store.Groups.Upsert(ctx, domain.Group{
			GroupJID:         g.GroupJID,
			Subject:          stringPtr(g.Subject),
			Description:      stringPtr(g.Description),
			OwnerJID:         stringPtr(g.OwnerJID),
			ParticipantCount: &participantCount,
			IsAnnounce:       &isAnnounce,
			IsLocked:         &isLocked,
			CreatedAtWA:      createdAtWA,
			FirstSeenAt:      now,
			UpdatedAt:        now,
		}); err != nil {
			return 0, 0, 0, err
		}
		groupCount++
		for _, m := range g.Members {
			if m.LID == "" {
				continue
			}
			// Seed an identity for every participant so group senders resolve to a
			// name/phone (the previous backfill only covered the contact store, so
			// most members had no identity row). Name is best-effort and COALESCEd,
			// so message push-name capture still upgrades it later.
			if err := s.store.Identities.Upsert(ctx, domain.Identity{
				LID:         m.LID,
				PhoneNumber: stringPtr(m.PhoneNumber),
				PhoneJID:    stringPtr(m.JID),
				Name:        stringPtr(m.Name),
				FirstSeenAt: now,
				UpdatedAt:   now,
			}); err != nil {
				return 0, 0, 0, err
			}
			if err := s.store.GroupMembers.Upsert(ctx, domain.GroupMember{
				SessionID:   sessionID,
				GroupJID:    g.GroupJID,
				LID:         m.LID,
				Tag:         stringPtr(m.Tag),
				Role:        m.Role,
				FirstSeenAt: now,
				LastSeenAt:  now,
			}); err != nil {
				return 0, 0, 0, err
			}
			memberCount++
		}
	}
	return contactCount, groupCount, memberCount, nil
}
