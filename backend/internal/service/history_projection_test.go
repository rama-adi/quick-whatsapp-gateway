package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

// TestProjectHistoryBatchFlattensPayload checks the wire-to-store mapping: chat
// types come from the JID, kinds default from fromMe, statuses are validated,
// media keeps metadata only, and the retention cutoff is derived from
// RETENTION_DAYS.
func TestProjectHistoryBatchFlattensPayload(t *testing.T) {
	fs := &fakeProjectionStore{}
	const now = int64(100 * dayMs)
	c := NewEventProjectionConsumer(fs, func() int64 { return now }).WithRetentionDays(30)
	payload := apitypes.HistoryBatchPayload{
		RunID: "hsr_1", AccountJID: "62811@s.whatsapp.net", SyncType: "recent", ChunkOrder: 1, Part: 1, Parts: 1,
		SnapshotAt: 5,
		Conversations: []apitypes.HistoryConversation{{
			ChatJID: "120363@g.us", Type: "dm", UnreadCount: func() *int { v := 2; return &v }(), MutedUntil: 99,
			Participants: []apitypes.HistoryParticipant{{LID: "1@lid", Role: "admin"}},
			Messages: []apitypes.HistoryMessage{
				{MessagePayload: apitypes.MessagePayload{WAMessageID: "A", ChatJID: "120363@g.us", SenderJID: "1@lid", Type: "image",
					HasMedia: true, Media: &domain.MediaMeta{ID: "att_1", URL: "https://x", Mimetype: "image/jpeg"}, Timestamp: 1}},
				{MessagePayload: apitypes.MessagePayload{WAMessageID: "B", FromMe: true, Type: "text", Body: "hi", Timestamp: 2}, Status: "read"},
				{MessagePayload: apitypes.MessagePayload{WAMessageID: "C", FromMe: true, Type: "text", Body: "x", Timestamp: 3}, Status: "bogus"},
				{Kind: "message.edited", MessagePayload: apitypes.MessagePayload{TargetID: "B", Body: "", Timestamp: 4}},
			},
		}},
	}
	if err := c.ConsumeCommittedEvent(context.Background(), domain.Event{
		ID: "evt_1", Type: domain.EventHistoryBatch, Session: "sess_1", Organization: "org_1", Timestamp: 9, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if len(fs.historyBatches) != 1 || len(fs.insertedMessages) != 0 || len(fs.upsertChats) != 0 {
		t.Fatalf("history must bypass the live projection: batches=%d live msgs=%d chats=%d",
			len(fs.historyBatches), len(fs.insertedMessages), len(fs.upsertChats))
	}
	b := fs.historyBatches[0]
	if b.EventID != "evt_1" || b.RunID != "hsr_1" || b.RetentionCutoff != now-30*dayMs || b.SnapshotAt != 5 || b.Now != now {
		t.Fatalf("batch header = %+v", b)
	}
	conv := b.Conversations[0]
	if conv.Type != domain.ChatGroup || conv.MutedUntil == nil || *conv.MutedUntil != 99 || conv.Participants[0].Role != domain.RoleAdmin {
		t.Fatalf("conversation = %+v", conv)
	}
	a, mine, bogus, edit := conv.Messages[0], conv.Messages[1], conv.Messages[2], conv.Messages[3]
	if a.Kind != store.HistoryKindMessage || a.SenderLID != "1@lid" || a.SenderJID != "" ||
		a.MediaMeta == nil || a.MediaMeta.URL != "" || a.MediaMeta.ID != "" || a.MediaMeta.Mimetype != "image/jpeg" {
		t.Fatalf("media message = %+v / %+v", a, a.MediaMeta)
	}
	if mine.Kind != store.HistoryKindFromMe || mine.Status == nil || *mine.Status != domain.MessageRead {
		t.Fatalf("own message = %+v", mine)
	}
	if bogus.Status != nil {
		t.Fatalf("invalid status must be dropped, got %v", *bogus.Status)
	}
	if edit.Kind != store.HistoryKindEdited || edit.TargetID != "B" || edit.Body != "" {
		t.Fatalf("edit = %+v", edit)
	}
	var raw apitypes.MessagePayload
	if err := json.Unmarshal(mine.RawJSON, &raw); err != nil || raw.WAMessageID != "B" {
		t.Fatalf("raw json = %s err=%v", mine.RawJSON, err)
	}
}

// TestProjectHistoryEventsSkipBadPayloads makes undecodable or incomplete
// history events logged no-ops: returning an error would block the session's
// ordered event stream forever.
func TestProjectHistoryEventsSkipBadPayloads(t *testing.T) {
	fs := &fakeProjectionStore{}
	c := NewEventProjectionConsumer(fs, func() int64 { return 1 })
	for _, ev := range []domain.Event{
		{ID: "e1", Type: domain.EventHistoryBatch, Session: "s", Payload: json.RawMessage(`{"runId":`)},
		{ID: "e2", Type: domain.EventHistoryBatch, Session: "s", Payload: json.RawMessage(`{"conversations":[]}`)},
		{ID: "e3", Type: domain.EventHistorySync, Session: "s", Payload: json.RawMessage(`[]`)},
		{ID: "e4", Type: domain.EventHistorySync, Session: "s", Payload: json.RawMessage(`{"runId":"r","phase":"bogus"}`)},
	} {
		if err := c.ConsumeCommittedEvent(context.Background(), ev); err != nil {
			t.Fatalf("%s: %v", ev.ID, err)
		}
	}
	if len(fs.historyBatches)+len(fs.historySyncs) != 0 {
		t.Fatalf("bad payloads reached the store: %d batches, %d syncs", len(fs.historyBatches), len(fs.historySyncs))
	}
	if err := c.ConsumeCommittedEvent(context.Background(), domain.Event{
		ID: "e5", Type: domain.EventHistorySync, Session: "s",
		Payload: json.RawMessage(`{"runId":"r","phase":"progress","progress":40,"chunks":2,"messages":9}`),
	}); err != nil {
		t.Fatal(err)
	}
	if len(fs.historySyncs) != 1 || fs.historySyncs[0].Chunks != 2 || *fs.historySyncs[0].Progress != 40 {
		t.Fatalf("sync = %+v", fs.historySyncs)
	}
}
