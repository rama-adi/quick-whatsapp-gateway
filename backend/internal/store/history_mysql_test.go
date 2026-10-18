package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func historyFixture(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()
	_, session := lifecycleFixture(t, db)
	if err := NewGatewayAssignmentRepo(db).CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM poll_votes WHERE session_id=?`,
			`DELETE FROM polls WHERE session_id=?`,
			`DELETE FROM messages WHERE session_id=?`,
			`DELETE FROM chats WHERE session_id=?`,
			`DELETE FROM whatsapp_group_members WHERE session_id=?`,
			`DELETE FROM history_sync_batches WHERE session_id=?`,
			`DELETE FROM history_sync_runs WHERE session_id=?`,
		} {
			if _, err := db.Exec(q, session.ID); err != nil {
				t.Error(err)
			}
		}
	})
	return session.ID
}

func i(v int) *int { return &v }

// TestHistoryApplyBatchMySQL covers the projection rules end to end: the
// ledger makes redelivery a no-op, live rows win, ids carry the message time,
// retention drops old rows, chat unread respects snapshot freshness, edits and
// revokes land, and an already-closed poll never schedules a stale recap.
func TestHistoryApplyBatchMySQL(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	sessionID := historyFixture(t, db)
	suffix := strings.ToLower(sessionID[len(sessionID)-10:])
	groupJID := "1203630" + suffix + "@g.us"
	dmJID := "1" + suffix + "@lid"
	senderLID := "2" + suffix + "@lid"
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM whatsapp_identities WHERE lid IN (?,?)`, dmJID, senderLID)
		_, _ = db.Exec(`DELETE FROM whatsapp_groups WHERE group_jid=?`, groupJID)
	})
	chats := NewChatRepo(db)
	msgs := NewMessageRepo(db)

	// Live state before the import: the DM chat already saw a message newer
	// than the phone snapshot (so the snapshot unread must not apply), and one
	// history message already arrived live.
	if err := chats.Upsert(ctx, domain.Chat{SessionID: sessionID, ChatJID: dmJID, Type: domain.ChatDM, LastMessageAt: i64ptr(9_000)}); err != nil {
		t.Fatal(err)
	}
	if err := chats.UpdateFlags(ctx, sessionID, dmJID, false, false, nil, 1); err != nil {
		t.Fatal(err)
	}
	if err := msgs.Upsert(ctx, domain.Message{SessionID: sessionID, WAMessageID: "LIVE1", ChatJID: dmJID,
		Direction: domain.DirectionIn, Type: "text", Body: strptr("live body"), Timestamp: 4_000, CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}

	read := domain.MessageRead
	batch := HistoryBatch{
		EventID: "evt_" + domain.NewULID(), SessionID: sessionID, RunID: "hsr_test", AccountJID: "62811@s.whatsapp.net",
		SnapshotAt: 8_000, RetentionCutoff: 1_500, Now: 10_000,
		Contacts: []HistoryContact{{LID: senderLID, PushName: "Bob"}},
		Conversations: []HistoryConversation{
			{
				ChatJID: dmJID, Type: domain.ChatDM, Name: "Alice", UnreadCount: i(7), Archived: true,
				Messages: []HistoryMessage{
					{Kind: HistoryKindMessage, WAMessageID: "OLD", Type: "text", Body: "too old", Timestamp: 1_000},
					{Kind: HistoryKindMessage, WAMessageID: "LIVE1", Type: "text", Body: "history body", Timestamp: 4_000},
					{Kind: HistoryKindFromMe, WAMessageID: "MINE", FromMe: true, Type: "text", Body: "hello", Timestamp: 5_000, Status: &read},
					{Kind: HistoryKindEdited, TargetID: "MINE", Type: "text", Body: "hello (edited)", Timestamp: 5_500},
				},
			},
			{
				ChatJID: groupJID, Type: domain.ChatGroup, Name: "Team", UnreadCount: i(3), Pinned: true,
				Participants: []HistoryParticipant{{LID: senderLID, Role: domain.RoleAdmin}},
				Messages: []HistoryMessage{
					{Kind: HistoryKindMessage, WAMessageID: "POLL1", SenderLID: senderLID, Type: "poll", Body: "Lunch?", Timestamp: 6_000,
						Poll: &HistoryPoll{Name: "Lunch?", Options: []string{"Yes", "No"}, SelectableCount: 1, EndTime: 7_000}},
					{Kind: HistoryKindMessage, WAMessageID: "GONE", SenderLID: senderLID, Type: "text", Body: "oops", Timestamp: 6_500},
					{Kind: HistoryKindRevoked, TargetID: "GONE", SenderLID: senderLID, Type: "revoke", Timestamp: 6_600},
				},
			},
		},
	}
	repo := NewHistoryRepo(db)
	res, err := repo.ApplyBatch(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || res.Imported != 3 || res.Existing != 1 || res.Dropped != 1 {
		t.Fatalf("result = %+v", res)
	}
	again, err := repo.ApplyBatch(ctx, batch)
	if err != nil || again.Applied {
		t.Fatalf("redelivery = %+v err=%v", again, err)
	}

	live, err := msgs.GetByWAID(ctx, sessionID, "LIVE1")
	if err != nil || live.Origin != domain.OriginLive || *live.Body != "live body" {
		t.Fatalf("live row changed: %+v err=%v", live, err)
	}
	mine, err := msgs.GetByWAID(ctx, sessionID, "MINE")
	if err != nil {
		t.Fatal(err)
	}
	if mine.Origin != domain.OriginHistory || !mine.Edited || *mine.Body != "hello (edited)" ||
		mine.Status == nil || *mine.Status != domain.MessageRead || mine.Direction != domain.DirectionOut {
		t.Fatalf("imported row = %+v", mine)
	}
	if got := int64(ulid.MustParse(strings.TrimPrefix(mine.ID, domain.PrefixMessage)).Time()); got != 5_000 {
		t.Fatalf("imported id time = %d", got)
	}
	if _, err := msgs.GetByWAID(ctx, sessionID, "OLD"); err == nil {
		t.Fatal("message older than retention cutoff was stored")
	}
	gone, err := msgs.GetByWAID(ctx, sessionID, "GONE")
	if err != nil || !gone.Deleted {
		t.Fatalf("revoke not applied: %+v err=%v", gone, err)
	}

	dm, err := chats.Get(ctx, sessionID, dmJID)
	if err != nil {
		t.Fatal(err)
	}
	if dm.UnreadCount != 1 || dm.Archived || *dm.LastMessageAt != 9_000 {
		t.Fatalf("existing chat = %+v (unread must stay 1, flags insert-only, last only forward)", dm)
	}
	group, err := chats.Get(ctx, sessionID, groupJID)
	if err != nil {
		t.Fatal(err)
	}
	if group.UnreadCount != 3 || !group.Pinned || group.LastMessageAt == nil || *group.LastMessageAt != 6_500 {
		t.Fatalf("new chat = %+v", group)
	}

	var recap sql.NullInt64
	if err := db.QueryRow(`SELECT recap_emitted_at FROM polls WHERE session_id=? AND poll_message_id='POLL1'`, sessionID).Scan(&recap); err != nil {
		t.Fatal(err)
	}
	if !recap.Valid || recap.Int64 != 10_000 {
		t.Fatalf("closed poll recap_emitted_at = %+v, want suppressed at now", recap)
	}
	var role, name string
	if err := db.QueryRow(`SELECT role FROM whatsapp_group_members WHERE session_id=? AND group_jid=? AND lid=?`, sessionID, groupJID, senderLID).Scan(&role); err != nil || role != "admin" {
		t.Fatalf("member role = %q err=%v", role, err)
	}
	if err := db.QueryRow(`SELECT name FROM whatsapp_identities WHERE lid=?`, senderLID).Scan(&name); err != nil || name != "Bob" {
		t.Fatalf("identity name = %q err=%v", name, err)
	}

	run, err := repo.LatestRun(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if run.BatchesApplied != 1 || run.MessagesImported != 3 || run.MessagesExisting != 1 || run.MessagesDropped != 1 || run.Status != "running" {
		t.Fatalf("run = %+v", run)
	}
}

// TestHistoryApplyBatchMySQLFillOnlyAndAliasMerge keeps known names and links a
// phone-keyed DM chat to its LID once the batch teaches the mapping.
func TestHistoryApplyBatchMySQLFillOnlyAndAliasMerge(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	sessionID := historyFixture(t, db)
	suffix := strings.ToLower(sessionID[len(sessionID)-10:])
	lid := "3" + suffix + "@lid"
	phone := "6283" + suffix[:6] + "@s.whatsapp.net"
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM whatsapp_identities WHERE lid=?`, lid) })
	if err := NewIdentityRepo(db).Upsert(ctx, domain.Identity{LID: lid, Name: strptr("Known"), FirstSeenAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := NewChatRepo(db).Upsert(ctx, domain.Chat{SessionID: sessionID, ChatJID: phone, Type: domain.ChatDM, LastMessageAt: i64ptr(100)}); err != nil {
		t.Fatal(err)
	}
	_, err := NewHistoryRepo(db).ApplyBatch(ctx, HistoryBatch{
		EventID: "evt_" + domain.NewULID(), SessionID: sessionID, RunID: "hsr_alias", SnapshotAt: 50, Now: 200,
		Contacts: []HistoryContact{{LID: lid, PhoneJID: phone, PushName: "Phone Name"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var name, phoneJID string
	if err := db.QueryRow(`SELECT name, phone_jid FROM whatsapp_identities WHERE lid=?`, lid).Scan(&name, &phoneJID); err != nil {
		t.Fatal(err)
	}
	if name != "Known" || phoneJID != phone {
		t.Fatalf("identity name=%q phone=%q", name, phoneJID)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chats WHERE session_id=? AND chat_jid=?`, sessionID, lid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("lid chat count=%d err=%v", count, err)
	}
}

// TestHistoryApplySyncMySQL keeps gateway counters monotonic and mirrors the
// latest phase.
func TestHistoryApplySyncMySQL(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	sessionID := historyFixture(t, db)
	repo := NewHistoryRepo(db)
	for _, u := range []HistorySyncUpdate{
		{Phase: domain.HistoryPhaseStarted, SyncType: "initial_bootstrap", Chunks: 1, Messages: 10, Now: 100},
		{Phase: domain.HistoryPhaseProgress, Progress: i(40), Chunks: 3, Messages: 50, Now: 200},
		{Phase: domain.HistoryPhaseProgress, Chunks: 2, Messages: 20, Now: 150}, // replayed older counters
		{Phase: domain.HistoryPhaseCompleted, Chunks: 3, Messages: 50, CutoffAt: 7, Now: 300},
	} {
		u.SessionID, u.RunID, u.AccountJID = sessionID, "hsr_sync", "62811@s.whatsapp.net"
		if err := repo.ApplySync(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	run, err := repo.LatestRun(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" || run.Chunks != 3 || run.Messages != 50 || run.Progress == nil || *run.Progress != 40 ||
		run.FinishedAt == nil || *run.FinishedAt != 300 || run.StartedAt != 100 || run.UpdatedAt != 300 ||
		run.CutoffAt == nil || *run.CutoffAt != 7 || run.SyncType == nil || *run.SyncType != "initial_bootstrap" {
		t.Fatalf("run = %+v", run)
	}
	if _, err := repo.LatestRun(ctx, "sess_missing"); err == nil {
		t.Fatal("missing run must be not_found")
	}
}
