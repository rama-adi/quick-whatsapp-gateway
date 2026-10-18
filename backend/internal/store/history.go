package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// HistoryRepo projects WhatsApp history sync (history.batch / history.sync
// events) into the WA tables. Each batch is applied in one transaction guarded
// by the history_sync_batches ledger, so a redelivered event is a no-op.
//
// dbconn forces clientFoundRows=true, which makes RowsAffected useless for
// telling an ON DUPLICATE KEY insert from a duplicate. Every count below comes
// from explicit pre-selects inside the transaction instead.
type HistoryRepo struct {
	db *sql.DB
}

// NewHistoryRepo constructs a HistoryRepo.
func NewHistoryRepo(db *sql.DB) *HistoryRepo { return &HistoryRepo{db: db} }

// HistoryBatch is one decoded history.batch event, ready to apply.
type HistoryBatch struct {
	EventID    string
	SessionID  string
	RunID      string
	AccountJID string
	// SnapshotAt is when the phone produced the snapshot (epoch ms). A phone
	// unread count is applied only when it is at least as fresh as the chat's
	// stored last_message_at.
	SnapshotAt int64
	// RetentionCutoff drops messages older than this (epoch ms); 0 = keep all.
	RetentionCutoff int64
	Now             int64
	Contacts        []HistoryContact
	Conversations   []HistoryConversation
}

// HistoryContact is a push name and/or LID↔phone mapping.
type HistoryContact struct {
	LID      string
	PhoneJID string
	PushName string
}

// HistoryParticipant is a group member listed by the phone.
type HistoryParticipant struct {
	LID      string
	PhoneJID string
	Role     domain.GroupRole
}

// HistoryConversation is one chat with its phone-side state and messages.
type HistoryConversation struct {
	ChatJID      string
	Type         domain.ChatType
	Name         string
	Description  string
	LID          string
	PhoneJID     string
	UnreadCount  *int
	Archived     bool
	Pinned       bool
	MutedUntil   *int64
	Participants []HistoryParticipant
	Messages     []HistoryMessage
}

// HistoryMessageKind is the live event type a history message corresponds to.
type HistoryMessageKind string

// Kinds that change stored rows. Anything else (reactions, unknown kinds) only
// contributes sender identity.
const (
	HistoryKindMessage  HistoryMessageKind = HistoryMessageKind(domain.EventMessage)
	HistoryKindFromMe   HistoryMessageKind = HistoryMessageKind(domain.EventMessageFromMe)
	HistoryKindReply    HistoryMessageKind = HistoryMessageKind(domain.EventMessageInteractiveReply)
	HistoryKindEdited   HistoryMessageKind = HistoryMessageKind(domain.EventMessageEdited)
	HistoryKindRevoked  HistoryMessageKind = HistoryMessageKind(domain.EventMessageRevoked)
	HistoryKindPollVote HistoryMessageKind = HistoryMessageKind(domain.EventPollVote)
	HistoryKindReaction HistoryMessageKind = HistoryMessageKind(domain.EventMessageReaction)
)

func (k HistoryMessageKind) createsRow() bool {
	return k == HistoryKindMessage || k == HistoryKindFromMe || k == HistoryKindReply
}

// HistoryMessage is one imported message, already flattened for storage.
type HistoryMessage struct {
	Kind            HistoryMessageKind
	WAMessageID     string
	SenderLID       string
	SenderJID       string
	PushName        string
	FromMe          bool
	Type            string
	Body            string
	QuotedMessageID string
	Mentions        []string
	HasMedia        bool
	MediaMeta       *domain.MediaMeta
	Timestamp       int64
	RawJSON         []byte
	Status          *domain.MessageStatus
	TargetID        string
	Poll            *HistoryPoll
	SelectedOptions []byte
}

// HistoryPoll is the poll-creation metadata of an imported poll message.
type HistoryPoll struct {
	Name            string
	Options         []string
	SelectableCount int
	EndTime         int64
	HideVotes       bool
}

// HistoryBatchResult reports what one batch did.
type HistoryBatchResult struct {
	// Applied is false when the ledger already had this event (redelivery).
	Applied bool
	// Imported counts new message rows; Existing counts messages already stored
	// (live wins); Dropped counts messages older than the retention cutoff or
	// missing an id/chat.
	Imported, Existing, Dropped int
}

// historyInChunk bounds IN (...) lists so a batch never builds a huge statement.
const historyInChunk = 500

// ApplyBatch applies one history.batch in a single transaction:
// ledger check → identities (fill-only) → groups/members (insert-if-absent) →
// chats → messages (insert-if-absent, origin=history) → polls → edits/revokes/
// votes → ledger row + run counters.
func (r *HistoryRepo) ApplyBatch(ctx context.Context, b HistoryBatch) (HistoryBatchResult, error) {
	if b.EventID == "" || b.SessionID == "" || b.RunID == "" {
		return HistoryBatchResult{}, domain.ErrValidation("history batch requires event, session and run ids")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return HistoryBatchResult{}, fmt.Errorf("store: begin history batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var seen int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM history_sync_batches WHERE event_id=?`, b.EventID,
	).Scan(&seen); err != nil {
		return HistoryBatchResult{}, fmt.Errorf("store: history ledger check: %w", err)
	}
	if seen > 0 {
		return HistoryBatchResult{}, nil
	}
	if err := ensureHistoryRun(ctx, tx, b.SessionID, b.RunID, b.AccountJID, b.Now); err != nil {
		return HistoryBatchResult{}, err
	}

	ap := historyApplier{tx: tx, b: b}
	if err := ap.identities(ctx); err != nil {
		return HistoryBatchResult{}, err
	}
	if err := ap.groups(ctx); err != nil {
		return HistoryBatchResult{}, err
	}
	if err := ap.chats(ctx); err != nil {
		return HistoryBatchResult{}, err
	}
	if err := ap.messages(ctx); err != nil {
		return HistoryBatchResult{}, err
	}
	if err := ap.mutations(ctx); err != nil {
		return HistoryBatchResult{}, err
	}

	res := ap.res
	res.Applied = true
	// A plain INSERT (not IGNORE): a concurrent worker that applied the same
	// event first makes this fail on the primary key and roll back, so the batch
	// can never be counted twice.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO history_sync_batches
		 (event_id, session_id, run_id, messages_imported, messages_existing, messages_dropped, applied_at)
		 VALUES (?,?,?,?,?,?,?)`,
		b.EventID, b.SessionID, b.RunID, res.Imported, res.Existing, res.Dropped, b.Now,
	); err != nil {
		return HistoryBatchResult{}, fmt.Errorf("store: history ledger insert: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE history_sync_runs SET batches_applied=batches_applied+1,
		 messages_imported=messages_imported+?, messages_existing=messages_existing+?,
		 messages_dropped=messages_dropped+?, updated_at=GREATEST(updated_at, ?)
		 WHERE session_id=? AND run_id=?`,
		res.Imported, res.Existing, res.Dropped, b.Now, b.SessionID, b.RunID,
	); err != nil {
		return HistoryBatchResult{}, fmt.Errorf("store: history run counters: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return HistoryBatchResult{}, fmt.Errorf("store: commit history batch: %w", err)
	}
	return res, nil
}

func ensureHistoryRun(ctx context.Context, db dbExecQuerier, sessionID, runID, accountJID string, now int64) error {
	if _, err := db.ExecContext(ctx,
		`INSERT INTO history_sync_runs (session_id, run_id, account_jid, status, started_at, updated_at)
		 VALUES (?,?,?,'running',?,?)
		 ON DUPLICATE KEY UPDATE account_jid=COALESCE(account_jid, VALUES(account_jid))`,
		sessionID, runID, nullIfEmpty(accountJID), now, now,
	); err != nil {
		return fmt.Errorf("store: ensure history run: %w", err)
	}
	return nil
}

type historyApplier struct {
	tx  *sql.Tx
	b   HistoryBatch
	res HistoryBatchResult
	// canonical maps a conversation's chat JID to its stored (canonical) JID.
	canonical map[string]string
}

type identityFill struct {
	phoneJID string
	name     string
}

// identities fills whatsapp_identities from contacts, DM conversations, group
// participants and message senders. Fill-only: a known phone mapping or name
// is never overwritten by the (possibly older) phone snapshot. A newly learned
// LID↔phone mapping merges this session's phone-keyed DM chat into the LID chat.
func (a *historyApplier) identities(ctx context.Context) error {
	byLID := map[string]*identityFill{}
	var order []string
	phoneNames := map[string]string{}
	add := func(lid, phoneJID, name string) {
		switch {
		case strings.HasSuffix(lid, "@lid"):
			f := byLID[lid]
			if f == nil {
				f = &identityFill{}
				byLID[lid] = f
				order = append(order, lid)
			}
			if f.phoneJID == "" && strings.HasSuffix(phoneJID, "@s.whatsapp.net") {
				f.phoneJID = phoneJID
			}
			if f.name == "" {
				f.name = name
			}
		case name != "" && strings.HasSuffix(phoneJID, "@s.whatsapp.net"):
			if _, ok := phoneNames[phoneJID]; !ok {
				phoneNames[phoneJID] = name
			}
		}
	}
	for _, c := range a.b.Contacts {
		add(c.LID, c.PhoneJID, c.PushName)
	}
	for _, conv := range a.b.Conversations {
		if conv.Type == domain.ChatDM {
			lid, phone := conv.LID, conv.PhoneJID
			if lid == "" && strings.HasSuffix(conv.ChatJID, "@lid") {
				lid = conv.ChatJID
			}
			if phone == "" && strings.HasSuffix(conv.ChatJID, "@s.whatsapp.net") {
				phone = conv.ChatJID
			}
			add(lid, phone, "")
		}
		for _, p := range conv.Participants {
			add(p.LID, p.PhoneJID, "")
		}
		for _, m := range conv.Messages {
			if !m.FromMe {
				add(m.SenderLID, m.SenderJID, m.PushName)
			}
		}
	}
	if len(order) > 0 {
		known, err := a.knownPhoneMappings(ctx, order)
		if err != nil {
			return err
		}
		var newMappings [][2]string
		for _, lid := range order {
			f := byLID[lid]
			if _, err := a.tx.ExecContext(ctx,
				`INSERT INTO whatsapp_identities (lid, phone_number, phone_jid, name, first_seen_at, updated_at)
				 VALUES (?,?,?,?,?,?)
				 ON DUPLICATE KEY UPDATE
				   phone_number=COALESCE(phone_number, VALUES(phone_number)),
				   phone_jid=COALESCE(phone_jid, VALUES(phone_jid)),
				   name=IF(name IS NULL OR name='', VALUES(name), name)`,
				lid, nullIfPtrEmpty(domain.PhoneFromJID(f.phoneJID)), nullIfEmpty(f.phoneJID), nullIfEmpty(f.name),
				a.b.Now, a.b.Now,
			); err != nil {
				return fmt.Errorf("store: history identity: %w", err)
			}
			if f.phoneJID != "" && known[lid] == "" {
				newMappings = append(newMappings, [2]string{lid, f.phoneJID})
			}
		}
		if err := a.mergeNewMappings(ctx, newMappings); err != nil {
			return err
		}
	}
	for phoneJID, name := range phoneNames {
		if err := NewIdentityRepo(a.tx).FillNameByJID(ctx, phoneJID, name, a.b.Now); err != nil {
			return err
		}
	}
	return nil
}

// knownPhoneMappings returns lid -> stored phone_jid ("" when unknown).
func (a *historyApplier) knownPhoneMappings(ctx context.Context, lids []string) (map[string]string, error) {
	out := make(map[string]string, len(lids))
	for start := 0; start < len(lids); start += historyInChunk {
		chunk := lids[start:min(start+historyInChunk, len(lids))]
		rows, err := a.tx.QueryContext(ctx,
			`SELECT lid, COALESCE(phone_jid, '') FROM whatsapp_identities WHERE lid IN (`+placeholders(len(chunk))+`)`,
			anyArgs(chunk)...)
		if err != nil {
			return nil, fmt.Errorf("store: history identity pre-select: %w", err)
		}
		for rows.Next() {
			var lid, phone string
			if err := rows.Scan(&lid, &phone); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("store: history identity scan: %w", err)
			}
			out[lid] = phone
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// mergeNewMappings folds this session's phone-keyed DM chats into their LID
// chats for mappings learned from this batch. Only mappings with an existing
// phone-keyed chat need work; checking first keeps large contact lists cheap.
func (a *historyApplier) mergeNewMappings(ctx context.Context, mappings [][2]string) error {
	if len(mappings) == 0 {
		return nil
	}
	phones := make([]string, 0, len(mappings))
	for _, m := range mappings {
		phones = append(phones, m[1])
	}
	existing := map[string]bool{}
	for start := 0; start < len(phones); start += historyInChunk {
		chunk := phones[start:min(start+historyInChunk, len(phones))]
		args := append([]any{a.b.SessionID}, anyArgs(chunk)...)
		rows, err := a.tx.QueryContext(ctx,
			`SELECT chat_jid FROM chats WHERE session_id=? AND chat_jid IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return fmt.Errorf("store: history alias pre-select: %w", err)
		}
		for rows.Next() {
			var jid string
			if err := rows.Scan(&jid); err != nil {
				_ = rows.Close()
				return err
			}
			existing[jid] = true
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	for _, m := range mappings {
		if existing[m[1]] {
			if err := mergeDMChatAlias(ctx, a.tx, a.b.SessionID, m[0], m[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// groups fills group subject/description and inserts members that are not
// known yet. Live group events own roles, so an existing member is untouched.
func (a *historyApplier) groups(ctx context.Context) error {
	for _, conv := range a.b.Conversations {
		if conv.Type != domain.ChatGroup {
			continue
		}
		if _, err := a.tx.ExecContext(ctx,
			`INSERT INTO whatsapp_groups (group_jid, subject, description, first_seen_at, updated_at)
			 VALUES (?,?,?,?,?)
			 ON DUPLICATE KEY UPDATE subject=COALESCE(subject, VALUES(subject)),
			   description=COALESCE(description, VALUES(description))`,
			conv.ChatJID, nullIfEmpty(conv.Name), nullIfEmpty(conv.Description), a.b.Now, a.b.Now,
		); err != nil {
			return fmt.Errorf("store: history group: %w", err)
		}
		members := map[string]domain.GroupRole{}
		var order []string
		addMember := func(lid string, role domain.GroupRole) {
			if !strings.HasSuffix(lid, "@lid") {
				return
			}
			if _, ok := members[lid]; ok {
				return
			}
			switch role {
			case domain.RoleAdmin, domain.RoleSuperAdmin:
			default:
				role = domain.RoleMember
			}
			members[lid] = role
			order = append(order, lid)
		}
		for _, p := range conv.Participants {
			addMember(p.LID, p.Role)
		}
		for _, m := range conv.Messages {
			if !m.FromMe {
				addMember(m.SenderLID, domain.RoleMember)
			}
		}
		for _, lid := range order {
			if _, err := a.tx.ExecContext(ctx,
				`INSERT INTO whatsapp_group_members (session_id, group_jid, lid, role, first_seen_at, last_seen_at)
				 VALUES (?,?,?,?,?,?)
				 ON DUPLICATE KEY UPDATE id=id`,
				a.b.SessionID, conv.ChatJID, lid, string(members[lid]), a.b.Now, a.b.Now,
			); err != nil {
				return fmt.Errorf("store: history group member: %w", err)
			}
		}
	}
	return nil
}

// keptMessage reports whether a row-creating message survives the filters.
func (a *historyApplier) keptMessage(m HistoryMessage) bool {
	if m.WAMessageID == "" {
		return false
	}
	return a.b.RetentionCutoff <= 0 || m.Timestamp >= a.b.RetentionCutoff
}

// chats upserts each conversation. On an existing chat: unread_count takes
// the phone snapshot only when the snapshot is at least as fresh as the
// stored last_message_at (assigned BEFORE last_message_at, which MySQL
// evaluates left to right); last_message_at only moves forward and comes from
// messages actually kept in this batch, so a conversation whose messages were
// all cut off does not surface as an empty inbox row; name is fill-only;
// archived/pinned/muted are applied on insert only.
func (a *historyApplier) chats(ctx context.Context) error {
	a.canonical = make(map[string]string, len(a.b.Conversations))
	for _, conv := range a.b.Conversations {
		if conv.ChatJID == "" {
			continue
		}
		chatJID := conv.ChatJID
		if conv.Type == domain.ChatDM {
			c, err := canonicalDMChatJID(ctx, a.tx, chatJID)
			if err != nil {
				return err
			}
			chatJID = c
		}
		a.canonical[conv.ChatJID] = chatJID
		var last *int64
		for _, m := range conv.Messages {
			if m.Kind.createsRow() && a.keptMessage(m) && (last == nil || m.Timestamp > *last) {
				ts := m.Timestamp
				last = &ts
			}
		}
		var unread any
		insertUnread := 0
		if conv.UnreadCount != nil && *conv.UnreadCount >= 0 {
			unread = *conv.UnreadCount
			insertUnread = *conv.UnreadCount
		}
		if _, err := a.tx.ExecContext(ctx,
			`INSERT INTO chats (session_id, chat_jid, type, name, last_message_at, unread_count, archived, pinned, muted_until)
			 VALUES (?,?,?,?,?,?,?,?,?)
			 ON DUPLICATE KEY UPDATE
			   unread_count = CASE
			     WHEN ? IS NULL THEN unread_count
			     WHEN COALESCE(last_message_at, 0) <= ? THEN ?
			     ELSE unread_count END,
			   last_message_at = CASE
			     WHEN VALUES(last_message_at) IS NULL THEN last_message_at
			     WHEN last_message_at IS NULL THEN VALUES(last_message_at)
			     ELSE GREATEST(last_message_at, VALUES(last_message_at)) END,
			   name = COALESCE(name, VALUES(name))`,
			a.b.SessionID, chatJID, string(conv.Type), nullIfEmpty(conv.Name), nullInt64Ptr(last),
			insertUnread, conv.Archived, conv.Pinned, nullInt64Ptr(conv.MutedUntil),
			unread, a.b.SnapshotAt, unread,
		); err != nil {
			return fmt.Errorf("store: history chat: %w", err)
		}
	}
	return nil
}

// messages inserts row-creating messages that are not stored yet. Existing
// rows win (live capture, a backup import, or an earlier batch): they are
// counted but never modified.
func (a *historyApplier) messages(ctx context.Context) error {
	var ids []string
	for _, conv := range a.b.Conversations {
		for _, m := range conv.Messages {
			if m.Kind.createsRow() && a.keptMessage(m) && a.canonical[conv.ChatJID] != "" {
				ids = append(ids, m.WAMessageID)
			}
		}
	}
	stored, err := a.storedMessageIDs(ctx, ids)
	if err != nil {
		return err
	}
	seq := domain.NewMessageIDSequence()
	for _, conv := range a.b.Conversations {
		chatJID := a.canonical[conv.ChatJID]
		for _, m := range conv.Messages {
			if !m.Kind.createsRow() {
				continue
			}
			if chatJID == "" || !a.keptMessage(m) {
				a.res.Dropped++
				continue
			}
			if stored[m.WAMessageID] {
				a.res.Existing++
				continue
			}
			if err := a.insertMessage(ctx, seq.Next(m.Timestamp), chatJID, m); err != nil {
				return err
			}
			stored[m.WAMessageID] = true
			a.res.Imported++
			if m.Poll != nil {
				if err := a.insertPoll(ctx, chatJID, m); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (a *historyApplier) storedMessageIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	for start := 0; start < len(ids); start += historyInChunk {
		chunk := ids[start:min(start+historyInChunk, len(ids))]
		args := append([]any{a.b.SessionID}, anyArgs(chunk)...)
		rows, err := a.tx.QueryContext(ctx,
			`SELECT wa_message_id FROM messages WHERE session_id=? AND wa_message_id IN (`+placeholders(len(chunk))+`)`,
			args...)
		if err != nil {
			return nil, fmt.Errorf("store: history message pre-select: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out[id] = true
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (a *historyApplier) insertMessage(ctx context.Context, id, chatJID string, m HistoryMessage) error {
	var mentions, media any
	if len(m.Mentions) > 0 {
		raw, err := json.Marshal(m.Mentions)
		if err != nil {
			return err
		}
		mentions = string(raw)
	}
	if m.MediaMeta != nil {
		raw, err := json.Marshal(m.MediaMeta)
		if err != nil {
			return fmt.Errorf("store: marshal history media_meta: %w", err)
		}
		media = string(raw)
	}
	var rawJSON any
	if len(m.RawJSON) > 0 {
		rawJSON = string(m.RawJSON)
	}
	direction := domain.DirectionIn
	if m.FromMe {
		direction = domain.DirectionOut
	}
	var status any
	if m.FromMe && m.Status != nil {
		status = string(*m.Status)
	}
	if _, err := a.tx.ExecContext(ctx,
		`INSERT INTO messages
		 (id, session_id, wa_message_id, chat_jid, sender_lid, sender_jid, from_me, direction,
		  type, body, quoted_message_id, mentions, has_media, media_meta, status,
		  edited, deleted, timestamp, raw_json, created_at, origin)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,0,?,?,?,'history')
		 ON DUPLICATE KEY UPDATE id=id`,
		id, a.b.SessionID, m.WAMessageID, chatJID, nullIfEmpty(m.SenderLID), nullIfEmpty(m.SenderJID),
		m.FromMe, string(direction), m.Type, nullIfEmpty(m.Body), nullIfEmpty(m.QuotedMessageID),
		mentions, m.HasMedia, media, status, m.Timestamp, rawJSON, a.b.Now,
	); err != nil {
		return fmt.Errorf("store: history message: %w", err)
	}
	return nil
}

// insertPoll records poll metadata if absent. A poll whose end time already
// passed gets recap_emitted_at=now, so importing an old poll never fires a
// stale poll.recap webhook.
func (a *historyApplier) insertPoll(ctx context.Context, chatJID string, m HistoryMessage) error {
	options, err := json.Marshal(m.Poll.Options)
	if err != nil {
		return err
	}
	selectable := m.Poll.SelectableCount
	if selectable <= 0 {
		selectable = 1
	}
	var endTime, recapAt any
	if m.Poll.EndTime > 0 {
		endTime = m.Poll.EndTime
		if m.Poll.EndTime <= a.b.Now {
			recapAt = a.b.Now
		}
	}
	if _, err := a.tx.ExecContext(ctx,
		`INSERT INTO polls (session_id, poll_message_id, chat_jid, name, options, selectable_count,
		  end_time, hide_votes, recap_emitted_at, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)
		 ON DUPLICATE KEY UPDATE id=id`,
		a.b.SessionID, m.WAMessageID, chatJID, nullIfEmpty(m.Poll.Name), string(options), selectable,
		endTime, m.Poll.HideVotes, recapAt, a.b.Now, a.b.Now,
	); err != nil {
		return fmt.Errorf("store: history poll: %w", err)
	}
	return nil
}

// mutations applies edits, revokes and poll votes after all rows exist, in
// message order. An edit only lands on a row that was not edited yet (a live
// edit is newer than the phone snapshot); revokes and votes are idempotent.
func (a *historyApplier) mutations(ctx context.Context) error {
	for _, conv := range a.b.Conversations {
		for _, m := range conv.Messages {
			switch m.Kind {
			case HistoryKindEdited:
				if m.TargetID == "" {
					continue
				}
				if _, err := a.tx.ExecContext(ctx,
					`UPDATE messages SET body=?, edited=1 WHERE session_id=? AND wa_message_id=? AND edited=0`,
					m.Body, a.b.SessionID, m.TargetID,
				); err != nil {
					return fmt.Errorf("store: history edit: %w", err)
				}
			case HistoryKindRevoked:
				if m.TargetID == "" {
					continue
				}
				if _, err := a.tx.ExecContext(ctx,
					`UPDATE messages SET deleted=1 WHERE session_id=? AND wa_message_id=?`,
					a.b.SessionID, m.TargetID,
				); err != nil {
					return fmt.Errorf("store: history revoke: %w", err)
				}
			case HistoryKindPollVote:
				voter := m.SenderLID
				if voter == "" {
					voter = m.SenderJID
				}
				if m.TargetID == "" || voter == "" || len(m.SelectedOptions) == 0 || !a.keptVote(m) {
					continue
				}
				var rawJSON any
				if len(m.RawJSON) > 0 {
					rawJSON = string(m.RawJSON)
				}
				if _, err := a.tx.ExecContext(ctx,
					`INSERT IGNORE INTO poll_votes (session_id, poll_message_id, voter_lid, selected_options, timestamp, raw_json)
					 VALUES (?,?,?,?,?,?)`,
					a.b.SessionID, m.TargetID, voter, string(m.SelectedOptions), m.Timestamp, rawJSON,
				); err != nil {
					return fmt.Errorf("store: history poll vote: %w", err)
				}
			}
		}
	}
	return nil
}

func (a *historyApplier) keptVote(m HistoryMessage) bool {
	return a.b.RetentionCutoff <= 0 || m.Timestamp >= a.b.RetentionCutoff
}

// HistorySyncUpdate is one history.sync lifecycle event.
type HistorySyncUpdate struct {
	SessionID     string
	RunID         string
	Phase         string
	AccountJID    string
	SyncType      string
	Progress      *int
	CutoffAt      int64
	Chunks        int
	Batches       int
	Conversations int
	Messages      int
	Skipped       int
	Error         string
	Now           int64
}

// ApplySync records a history.sync event on its run row. Gateway counters are
// cumulative, so they only grow (GREATEST); status mirrors the latest phase.
// Replaying the same event writes the same values.
func (r *HistoryRepo) ApplySync(ctx context.Context, u HistorySyncUpdate) error {
	if u.SessionID == "" || u.RunID == "" {
		return domain.ErrValidation("history sync requires session and run ids")
	}
	status := "running"
	var finished, errText any
	switch u.Phase {
	case domain.HistoryPhaseCompleted:
		status, finished = "completed", u.Now
	case domain.HistoryPhaseFailed:
		status, finished, errText = "failed", u.Now, nullIfEmpty(u.Error)
	case domain.HistoryPhaseStarted, domain.HistoryPhaseProgress:
	default:
		return domain.ErrValidation("unknown history sync phase")
	}
	var progress any
	if u.Progress != nil {
		progress = min(max(*u.Progress, 0), 100)
	}
	var cutoff any
	if u.CutoffAt > 0 {
		cutoff = u.CutoffAt
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO history_sync_runs
		 (session_id, run_id, account_jid, status, sync_type, progress, cutoff_at, chunks, batches,
		  conversations, messages, skipped, error, started_at, updated_at, finished_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON DUPLICATE KEY UPDATE
		   account_jid=COALESCE(account_jid, VALUES(account_jid)),
		   status=VALUES(status),
		   sync_type=COALESCE(VALUES(sync_type), sync_type),
		   progress=COALESCE(VALUES(progress), progress),
		   cutoff_at=COALESCE(VALUES(cutoff_at), cutoff_at),
		   chunks=GREATEST(chunks, VALUES(chunks)),
		   batches=GREATEST(batches, VALUES(batches)),
		   conversations=GREATEST(conversations, VALUES(conversations)),
		   messages=GREATEST(messages, VALUES(messages)),
		   skipped=GREATEST(skipped, VALUES(skipped)),
		   error=VALUES(error),
		   updated_at=GREATEST(updated_at, VALUES(updated_at)),
		   finished_at=VALUES(finished_at)`,
		u.SessionID, u.RunID, nullIfEmpty(u.AccountJID), status, nullIfEmpty(u.SyncType), progress, cutoff,
		u.Chunks, u.Batches, u.Conversations, u.Messages, u.Skipped, errText, u.Now, u.Now, finished,
	); err != nil {
		return fmt.Errorf("store: apply history sync: %w", err)
	}
	return nil
}

// LatestRun returns the session's most recently started run, or not_found.
func (r *HistoryRepo) LatestRun(ctx context.Context, sessionID string) (domain.HistorySyncRun, error) {
	var (
		run                       domain.HistorySyncRun
		account, syncType, errMsg sql.NullString
		progress                  sql.NullInt32
		cutoff, finished          sql.NullInt64
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT run_id, account_jid, status, sync_type, progress, cutoff_at, chunks, batches,
		  conversations, messages, skipped, batches_applied, messages_imported, messages_existing,
		  messages_dropped, error, started_at, updated_at, finished_at
		 FROM history_sync_runs WHERE session_id=? ORDER BY started_at DESC, id DESC LIMIT 1`,
		sessionID,
	).Scan(&run.RunID, &account, &run.Status, &syncType, &progress, &cutoff, &run.Chunks, &run.Batches,
		&run.Conversations, &run.Messages, &run.Skipped, &run.BatchesApplied, &run.MessagesImported,
		&run.MessagesExisting, &run.MessagesDropped, &errMsg, &run.StartedAt, &run.UpdatedAt, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HistorySyncRun{}, notFound(err, "history sync run")
	}
	if err != nil {
		return domain.HistorySyncRun{}, fmt.Errorf("store: latest history run: %w", err)
	}
	run.SessionID = sessionID
	run.AccountJID = stringPtrFromNull(account)
	run.SyncType = stringPtrFromNull(syncType)
	run.Error = stringPtrFromNull(errMsg)
	run.CutoffAt = int64PtrFromNull(cutoff)
	run.FinishedAt = int64PtrFromNull(finished)
	if progress.Valid {
		p := int(progress.Int32)
		run.Progress = &p
	}
	return run, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func anyArgs(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfPtrEmpty(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func nullInt64Ptr(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
