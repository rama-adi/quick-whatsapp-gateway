package store

import (
	"context"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// TestChatUpsertMySQLKeepsUnreadAndNullTimestamps pins the live chat upsert:
// a message-driven upsert must not reset the unread counter or the user's
// flags, last_message_at only moves forward, and a NULL input never turns a
// stored NULL into 0 (which would surface a never-messaged chat in the inbox).
func TestChatUpsertMySQLKeepsUnreadAndNullTimestamps(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	sessionID := domain.NewSessionID()
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM chats WHERE session_id=?`, sessionID); err != nil {
			t.Error(err)
		}
	})
	repo := NewChatRepo(db)
	const group = "120363000000000001@g.us"

	// A metadata-only upsert (no timestamp) creates the row with NULL.
	if err := repo.Upsert(ctx, domain.Chat{SessionID: sessionID, ChatJID: group, Type: domain.ChatGroup}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Upsert(ctx, domain.Chat{SessionID: sessionID, ChatJID: group, Type: domain.ChatGroup}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, sessionID, group)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastMessageAt != nil {
		t.Fatalf("NULL last_message_at became %d", *got.LastMessageAt)
	}

	if err := repo.Upsert(ctx, domain.Chat{SessionID: sessionID, ChatJID: group, Type: domain.ChatGroup, LastMessageAt: i64ptr(2000)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateFlags(ctx, sessionID, group, true, true, i64ptr(9999), 4); err != nil {
		t.Fatal(err)
	}
	// Older, NULL and newer message-driven upserts.
	for _, ts := range []*int64{i64ptr(1000), nil, i64ptr(3000)} {
		if err := repo.Upsert(ctx, domain.Chat{SessionID: sessionID, ChatJID: group, Type: domain.ChatGroup, LastMessageAt: ts}); err != nil {
			t.Fatal(err)
		}
	}
	got, err = repo.Get(ctx, sessionID, group)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastMessageAt == nil || *got.LastMessageAt != 3000 {
		t.Fatalf("last_message_at = %v, want 3000", got.LastMessageAt)
	}
	if got.UnreadCount != 4 || !got.Archived || !got.Pinned || got.MutedUntil == nil || *got.MutedUntil != 9999 {
		t.Fatalf("live upsert clobbered unread/flags: %+v", got)
	}
}
