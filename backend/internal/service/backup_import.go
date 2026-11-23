package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/backup"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

const (
	// quotaWindowMs is the per-session import cooldown for non-admins (once/24h).
	quotaWindowMs = 24 * 60 * 60 * 1000
	// importLease is how long a running import's lease lasts without a
	// heartbeat; importHeartbeat renews it well before it expires. A row
	// whose lease expired is marked interrupted by the maintenance reaper.
	importLease     = 2 * time.Minute
	importHeartbeat = 30 * time.Second
)

// backupImportDir is the managed directory for decrypted backup databases.
// Files are named <job id>.db, so the reaper can remove every file whose job
// no longer holds a live lease (left behind by a crash).
var backupImportDir = filepath.Join(os.TempDir(), "qwg-backup-imports")

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
// into a session's chats/messages/identities/groups. Decryption, ownership, quota,
// and the durable running-row reservation happen inline; after StartImport returns,
// a detached goroutine parses the staged SQLite file and records terminal progress
// in backfill_imports.
//
// Import is idempotent at repository upsert keys, but a failed job may leave a
// valid partial prefix that a later run reconciles. The request context does not
// cancel accepted background work. The running row names this process as its
// owner and carries a lease the job renews every importHeartbeat; if the process
// dies, the maintenance reaper marks the row interrupted once the lease expires
// and removes the staged plaintext file. A live running row is the per-session
// concurrency gate; succeeded rows are the durable source for the daily quota.
type BackupImportService struct {
	store   *store.Store
	log     *slog.Logger
	clock   func() int64
	owner   string
	dir     string
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
		owner:   "api-" + domain.NewULID(),
		dir:     backupImportDir,
		decrypt: backup.DecryptMsgstore,
		open:    func(path string) (backupReader, error) { return backup.Open(path) },
	}
}

// StartImport validates ownership and quota, decrypts the upload, stages a private
// temporary database, and inserts the running job before launching background
// work. Failures before insertion remove staged plaintext and have no quota effect;
// success returns only after durable job visibility, not after import completion.
func (s *BackupImportService) StartImport(
	ctx context.Context,
	organizationID, sessionID string,
	isSuperAdmin bool,
	ciphertext []byte,
	key string,
) (domain.BackfillImport, error) {
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
		return domain.BackfillImport{}, domain.ErrValidation(
			"could not decrypt backup (check the key and file): " + err.Error())
	}

	now := s.clock()

	running, err := s.store.BackfillImports.HasLiveRunning(ctx, sessionID, now)
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
			return domain.BackfillImport{}, domain.ErrRateLimited(
				"a backup can be imported once per day per session; try again later")
		}
	}

	job := domain.BackfillImport{
		ID:             domain.NewPrefixedID("bfi_"),
		SessionID:      sess.ID,
		OrganizationID: sess.OrganizationID,
		Source:         "crypt15",
		Status:         "running",
		CreatedAt:      now,
		Owner:          s.owner,
		LeaseUntil:     now + importLease.Milliseconds(),
	}
	if err := s.store.BackfillImports.Insert(ctx, job); err != nil {
		return domain.BackfillImport{}, err
	}
	// Stage the decrypted DB under the job id only after the leased row exists,
	// so the reaper never mistakes a file being staged for a leftover.
	path, err := s.writeTempDB(job.ID, plain)
	if err != nil {
		s.finishFailed(context.WithoutCancel(ctx), job, fmt.Errorf("stage backup: %w", err))
		return domain.BackfillImport{}, domain.ErrInternal("could not stage backup: " + err.Error())
	}

	go s.runImport(context.Background(), job, path)
	return job, nil
}

// ImportStatus returns the latest import for a session (ownership-checked).
func (s *BackupImportService) ImportStatus(
	ctx context.Context,
	organizationID, sessionID string,
	isSuperAdmin bool,
) (domain.BackfillImport, error) {
	sess, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return domain.BackfillImport{}, err
	}
	if !isSuperAdmin && sess.OrganizationID != organizationID {
		return domain.BackfillImport{}, domain.ErrNotFound("session not found")
	}
	return s.store.BackfillImports.LatestForSession(ctx, sessionID)
}

// writeTempDB materializes decrypted SQLite bytes for the backup reader in the
// managed directory. Any write or close failure removes the partial file;
// ownership of a successful path transfers to runImport, which removes it on
// completion (the reaper removes it after a crash).
func (s *BackupImportService) writeTempDB(jobID string, plain []byte) (string, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(s.dir, jobID+".db")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(plain); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// runImport owns the accepted job and staged file until one terminal status has
// been recorded. It continues independently of the initiating HTTP request and
// renews the job's lease while it works; if the lease is lost (the reaper
// interrupted the row) the import stops. Parse/upsert failures are captured on
// the job rather than returned to a caller.
func (s *BackupImportService) runImport(ctx context.Context, job domain.BackfillImport, path string) {
	defer func() { _ = os.Remove(path) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.heartbeat(ctx, cancel, job)

	db, err := s.open(path)
	if err != nil {
		s.finishFailed(ctx, job, err)
		return
	}
	defer func() { _ = db.Close() }()

	fp := db.Fingerprint()
	job.SchemaFingerprint = &fp

	err = s.importAll(ctx, db, job.SessionID, &job)
	finished := s.clock()
	job.FinishedAt = &finished
	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
		s.log.WarnContext(ctx, "backup import failed", "session", job.SessionID, "job", job.ID, "err", err)
		s.finishJob(ctx, job)
		return
	}
	job.Status = "succeeded"
	s.log.InfoContext(ctx, "backup import finished", "session", job.SessionID, "job", job.ID,
		"chats", job.Chats, "messages", job.Messages, "identities", job.Identities,
		"groups", job.Groups, "members", job.GroupMembers)
	s.finishJob(ctx, job)
}

// heartbeat renews the job's lease until ctx ends. A lost lease cancels the
// import: another process (the reaper) already ended the row.
func (s *BackupImportService) heartbeat(ctx context.Context, cancel context.CancelFunc, job domain.BackfillImport) {
	ticker := time.NewTicker(importHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := s.clock()
		owned, err := s.store.BackfillImports.Heartbeat(ctx, job.ID, s.owner, now, now+importLease.Milliseconds())
		if err != nil {
			s.log.WarnContext(ctx, "backup import heartbeat failed", "job", job.ID, "err", err)
			continue
		}
		if !owned {
			s.log.WarnContext(ctx, "backup import lost its lease; stopping", "job", job.ID)
			cancel()
			return
		}
	}
}

// finishJob records the terminal job row, retrying briefly. If every attempt
// fails the lease expires and the reaper marks the row interrupted, so the job
// never stays running.
func (s *BackupImportService) finishJob(ctx context.Context, job domain.BackfillImport) {
	job.Owner = s.owner
	// The import context may already be cancelled (lost lease); recording
	// the outcome must still be attempted.
	ctx = context.WithoutCancel(ctx)
	var err error
	for attempt, delay := 0, 500*time.Millisecond; attempt < 4; attempt, delay = attempt+1, delay*2 {
		if err = s.store.BackfillImports.Finish(ctx, job); err == nil {
			return
		}
		var apiErr *domain.APIError
		if errors.As(err, &apiErr) && apiErr.Code == domain.CodeNotFound {
			break // another owner holds the row or it already finished
		}
		time.Sleep(delay)
	}
	s.log.ErrorContext(ctx, "backup import: record result failed; the lease reaper ends the job",
		"job", job.ID, "status", job.Status, "err", err)
}

func (s *BackupImportService) finishFailed(ctx context.Context, job domain.BackfillImport, cause error) {
	finished := s.clock()
	job.FinishedAt = &finished
	job.Status = "failed"
	job.Error = cause.Error()
	s.log.WarnContext(ctx, "backup import failed", "session", job.SessionID, "job", job.ID, "err", cause)
	s.finishJob(ctx, job)
}

// ReapInterrupted marks running imports whose lease expired as interrupted
// and removes staged files that no live import owns (left by a crash). It is
// safe on every replica and at every boot.
func (s *BackupImportService) ReapInterrupted(ctx context.Context) error {
	now := s.clock()
	n, err := s.store.BackfillImports.InterruptExpired(ctx, now)
	if err != nil {
		return err
	}
	if n > 0 {
		s.log.WarnContext(ctx, "backup imports interrupted after their lease expired", "imports", n)
	}
	live, err := s.store.BackfillImports.LiveIDs(ctx, now)
	if err != nil {
		return err
	}
	keep := make(map[string]bool, len(live))
	for _, id := range live {
		keep[id+".db"] = true
	}
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || keep[entry.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.log.WarnContext(ctx, "remove stale backup staging file failed", "file", entry.Name(), "err", err)
		}
	}
	return nil
}

// importAll upserts every supported entity from the backup, accumulating counts
// onto job. Identities/groups/members are imported before chats/messages so
// senders resolve and group subjects are present. Any error aborts (the job is
// marked failed); partial counts so far are preserved on the row.
func (s *BackupImportService) importAll(
	ctx context.Context,
	db backupReader,
	sessionID string,
	job *domain.BackfillImport,
) error {
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

	// Ids carry each message's own timestamp so imported rows interleave with
	// live rows in timeline order (timelines sort by messages.id).
	ids := domain.NewMessageIDSequence()
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
			ID:              ids.Next(m.TimestampMs),
			Origin:          domain.OriginBackup,
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
