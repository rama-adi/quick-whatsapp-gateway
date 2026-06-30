package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/backup"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

const (
	// quotaWindowMs is the per-session import cooldown for non-admins (once/24h).
	quotaWindowMs = 24 * 60 * 60 * 1000
	// runningStaleMs bounds how long a 'running' row blocks a new import; a job
	// older than this is presumed crashed and no longer holds the lock.
	runningStaleMs = 60 * 60 * 1000
)

// backupReader is the slice of *backup.DB the importer uses; injectable so the
// service is testable without a real SQLite file.
type backupReader interface {
	Fingerprint() string
	EachChat(context.Context, func(backup.Chat) error) error
	EachMessage(context.Context, func(backup.Message) error) error
	EachIdentity(context.Context, func(backup.Identity) error) error
	EachGroup(context.Context, func(backup.Group) error) error
	EachGroupMember(context.Context, func(backup.GroupMember) error) error
	Close() error
}

// BackupImportService imports a user-uploaded WhatsApp backup (msgstore.db.crypt15)
// into a session's chats/messages/identities/groups. The decryption happens inline
// (so a bad key/file fails fast as a validation error); the SQLite parse + upsert
// runs in a background goroutine whose progress is tracked durably in
// backfill_imports (also the source of truth for the once/24h-per-session quota).
type BackupImportService struct {
	store   *store.Store
	log     *slog.Logger
	clock   func() int64
	decrypt func(ciphertext []byte, key string) ([]byte, error)
	open    func(path string) (backupReader, error)
}

// NewBackupImportService constructs a BackupImportService wired to the real
// decrypt/open implementations.
func NewBackupImportService(s *store.Store, log *slog.Logger) *BackupImportService {
	if log == nil {
		log = slog.Default()
	}
	return &BackupImportService{
		store:   s,
		log:     log,
		clock:   domain.NowMs,
		decrypt: backup.DecryptMsgstore,
		open:    func(path string) (backupReader, error) { return backup.Open(path) },
	}
}

// StartImport validates ownership/quota, decrypts the upload inline, and kicks off
// the background import. Returns the created 'running' job.
func (s *BackupImportService) StartImport(ctx context.Context, organizationID, sessionID string, isSuperAdmin bool, ciphertext []byte, key string) (domain.BackfillImport, error) {
	sess, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return domain.BackfillImport{}, err
	}
	// Non-admins may only import into their own org's sessions; super_admins may
	// target any session.
	if !isSuperAdmin && sess.OrganizationID != organizationID {
		return domain.BackfillImport{}, domain.ErrNotFound("session not found")
	}

	// Decrypt inline so a wrong key / bad file fails fast as a 4xx, before we
	// reserve a job or touch the quota.
	plain, err := s.decrypt(ciphertext, key)
	if err != nil {
		return domain.BackfillImport{}, domain.ErrValidation("could not decrypt backup (check the key and file): " + err.Error())
	}

	now := s.clock()

	running, err := s.store.BackfillImports.HasRunningSince(ctx, sessionID, now-runningStaleMs)
	if err != nil {
		return domain.BackfillImport{}, err
	}
	if running {
		return domain.BackfillImport{}, domain.ErrConflict("a backup import is already running for this session")
	}

	if !isSuperAdmin {
		at, ok, err := s.store.BackfillImports.LastSuccessAt(ctx, sessionID)
		if err != nil {
			return domain.BackfillImport{}, err
		}
		if ok && now-at < quotaWindowMs {
			return domain.BackfillImport{}, domain.ErrRateLimited("a backup can be imported once per day per session; try again later")
		}
	}

	// Persist the decrypted DB to a temp file the background reader opens.
	path, err := writeTempDB(plain)
	if err != nil {
		return domain.BackfillImport{}, domain.ErrInternal("could not stage backup: " + err.Error())
	}

	job := domain.BackfillImport{
		ID:             domain.NewPrefixedID("bfi_"),
		SessionID:      sess.ID,
		OrganizationID: sess.OrganizationID,
		Source:         "crypt15",
		Status:         "running",
		CreatedAt:      now,
	}
	if err := s.store.BackfillImports.Insert(ctx, job); err != nil {
		_ = os.Remove(path)
		return domain.BackfillImport{}, err
	}

	go s.runImport(context.Background(), job, path)
	return job, nil
}

// ImportStatus returns the latest import for a session (ownership-checked).
func (s *BackupImportService) ImportStatus(ctx context.Context, organizationID, sessionID string, isSuperAdmin bool) (domain.BackfillImport, error) {
	sess, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return domain.BackfillImport{}, err
	}
	if !isSuperAdmin && sess.OrganizationID != organizationID {
		return domain.BackfillImport{}, domain.ErrNotFound("session not found")
	}
	return s.store.BackfillImports.LatestForSession(ctx, sessionID)
}

func writeTempDB(plain []byte) (string, error) {
	f, err := os.CreateTemp("", "msgstore-*.db")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(plain); err != nil {
		f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func (s *BackupImportService) runImport(ctx context.Context, job domain.BackfillImport, path string) {
	defer func() { _ = os.Remove(path) }()

	db, err := s.open(path)
	if err != nil {
		s.finishFailed(ctx, job, err)
		return
	}
	defer db.Close()

	fp := db.Fingerprint()
	job.SchemaFingerprint = &fp

	err = s.importAll(ctx, db, job.SessionID, &job)
	finished := s.clock()
	job.FinishedAt = &finished
	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
		s.log.WarnContext(ctx, "backup import failed", "session", job.SessionID, "job", job.ID, "err", err)
	} else {
		job.Status = "succeeded"
		s.log.InfoContext(ctx, "backup import finished", "session", job.SessionID, "job", job.ID,
			"chats", job.Chats, "messages", job.Messages, "identities", job.Identities,
			"groups", job.Groups, "members", job.GroupMembers)
	}
	if e := s.store.BackfillImports.Finish(ctx, job); e != nil {
		s.log.WarnContext(ctx, "backup import: record result failed", "job", job.ID, "err", e)
	}
}

func (s *BackupImportService) finishFailed(ctx context.Context, job domain.BackfillImport, cause error) {
	finished := s.clock()
	job.FinishedAt = &finished
	job.Status = "failed"
	job.Error = cause.Error()
	s.log.WarnContext(ctx, "backup import failed", "session", job.SessionID, "job", job.ID, "err", cause)
	if e := s.store.BackfillImports.Finish(ctx, job); e != nil {
		s.log.WarnContext(ctx, "backup import: record failure failed", "job", job.ID, "err", e)
	}
}

// importAll upserts every supported entity from the backup, accumulating counts
// onto job. Identities/groups/members are imported before chats/messages so
// senders resolve and group subjects are present. Any error aborts (the job is
// marked failed); partial counts so far are preserved on the row.
func (s *BackupImportService) importAll(ctx context.Context, db backupReader, sessionID string, job *domain.BackfillImport) error {
	now := s.clock()

	if err := db.EachIdentity(ctx, func(id backup.Identity) error {
		lid := id.LID
		if !isLID(lid) {
			return nil
		}
		if err := s.store.Identities.Upsert(ctx, domain.Identity{
			LID:         lid,
			PhoneNumber: stringPtr(id.Phone),
			PhoneJID:    stringPtr(id.PhoneJID),
			Name:        stringPtr(id.Name),
			FirstSeenAt: now,
			UpdatedAt:   now,
		}); err != nil {
			return err
		}
		job.Identities++
		return nil
	}); err != nil {
		return err
	}

	if err := db.EachGroup(ctx, func(g backup.Group) error {
		if g.JID == "" {
			return nil
		}
		if err := s.store.Groups.Upsert(ctx, domain.Group{
			GroupJID:    g.JID,
			Subject:     stringPtr(g.Subject),
			FirstSeenAt: now,
			UpdatedAt:   now,
		}); err != nil {
			return err
		}
		job.Groups++
		return nil
	}); err != nil {
		return err
	}

	if err := db.EachGroupMember(ctx, func(m backup.GroupMember) error {
		if m.GroupJID == "" || !isLID(m.LID) {
			return nil
		}
		if err := s.store.GroupMembers.Upsert(ctx, domain.GroupMember{
			SessionID:   sessionID,
			GroupJID:    m.GroupJID,
			LID:         m.LID,
			Tag:         stringPtr(m.Tag),
			Role:        domain.GroupRole(m.Role),
			FirstSeenAt: now,
			LastSeenAt:  now,
		}); err != nil {
			return err
		}
		job.GroupMembers++
		return nil
	}); err != nil {
		return err
	}

	if err := db.EachChat(ctx, func(c backup.Chat) error {
		if c.JID == "" {
			return nil
		}
		if err := s.store.Chats.Upsert(ctx, domain.Chat{
			SessionID:     sessionID,
			ChatJID:       c.JID,
			Type:          domain.ChatType(c.Type),
			Name:          stringPtr(c.Name),
			LastMessageAt: int64Ptr(c.LastMessageAt),
		}); err != nil {
			return err
		}
		job.Chats++
		return nil
	}); err != nil {
		return err
	}

	if err := db.EachMessage(ctx, func(m backup.Message) error {
		if m.WAMessageID == "" || m.ChatJID == "" {
			return nil
		}
		var mentions json.RawMessage
		if len(m.Mentions) > 0 {
			b, err := json.Marshal(m.Mentions)
			if err != nil {
				return err
			}
			mentions = b
		}
		var media *domain.MediaMeta
		if m.HasMedia {
			media = &domain.MediaMeta{Mimetype: m.MediaMime, Size: m.MediaSize, Filename: m.MediaName}
		}
		dir := domain.DirectionIn
		if m.FromMe {
			dir = domain.DirectionOut
		}
		senderLID := m.SenderLID
		senderJID := m.SenderJID
		if !isLID(senderLID) {
			if isLID(senderJID) {
				senderLID, senderJID = senderJID, ""
			} else {
				senderLID = ""
			}
		}
		if err := s.store.Messages.Upsert(ctx, domain.Message{
			SessionID:       sessionID,
			WAMessageID:     m.WAMessageID,
			ChatJID:         m.ChatJID,
			SenderLID:       stringPtr(senderLID),
			SenderJID:       stringPtr(senderJID),
			FromMe:          m.FromMe,
			Direction:       dir,
			Type:            m.Type,
			Body:            stringPtr(m.Body),
			QuotedMessageID: stringPtr(m.QuotedMessageID),
			Mentions:        mentions,
			HasMedia:        m.HasMedia,
			MediaMeta:       media,
			Timestamp:       m.TimestampMs,
			CreatedAt:       now,
		}); err != nil {
			return err
		}
		job.Messages++
		return nil
	}); err != nil {
		return err
	}

	return nil
}
