package historysync

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

const (
	ownJID   = "15550000001@s.whatsapp.net"
	alicePN  = "15550000002@s.whatsapp.net"
	aliceLID = "900000000000002@lid"
	bobLID   = "900000000000003@lid"
	bobPN    = "15550000003@s.whatsapp.net"
	groupJID = "120363000000000001@g.us"
)

var notifiedAt = time.UnixMilli(1_760_000_000_000).UTC()

func testParser(t *testing.T) Parser {
	t.Helper()
	own := types.NewJID("15550000001", types.DefaultUserServer)
	cli := whatsmeow.NewClient(&store.Device{ID: &own}, nil)
	return cli.ParseWebMessage
}

func input(t *testing.T, blob *waHistorySync.HistorySync) Input {
	return Input{
		SessionID: "sess_1", NotificationID: "NOTIF1", NotifiedAt: notifiedAt,
		RunID: "hsr_run", AccountJID: ownJID, Blob: blob, Parse: testParser(t),
	}
}

func webMsg(id, remote string, fromMe bool, participant string, tsSec uint64, msg *waE2E.Message) *waWeb.WebMessageInfo {
	key := &waCommon.MessageKey{ID: proto.String(id), RemoteJID: proto.String(remote), FromMe: proto.Bool(fromMe)}
	wm := &waWeb.WebMessageInfo{Key: key, MessageTimestamp: proto.Uint64(tsSec), Message: msg}
	if participant != "" {
		wm.Participant = proto.String(participant)
	}
	return wm
}

func text(s string) *waE2E.Message { return &waE2E.Message{Conversation: proto.String(s)} }

func hmsg(order uint64, wm *waWeb.WebMessageInfo) *waHistorySync.HistorySyncMsg {
	return &waHistorySync.HistorySyncMsg{MsgOrderID: proto.Uint64(order), Message: wm}
}

func sha(s string) []byte { sum := sha256.Sum256([]byte(s)); return sum[:] }

func TestConvertMapsMessagesLikeLive(t *testing.T) {
	base := uint64(notifiedAt.Unix()) - 3600
	poll := &waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{
		Name: proto.String("Lunch?"), SelectableOptionsCount: proto.Uint32(1),
		Options: []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("Yes")}, {OptionName: proto.String("No")}},
	}}
	pollMsg := webMsg("POLL1", groupJID, true, "", base+30, poll)
	pollMsg.PollUpdates = []*waWeb.PollUpdate{{
		PollUpdateMessageKey: &waCommon.MessageKey{ID: proto.String("VOTE1"), RemoteJID: proto.String(groupJID), Participant: proto.String(bobLID)},
		Vote:                 &waE2E.PollVoteMessage{SelectedOptions: [][]byte{sha("No")}},
		SenderTimestampMS:    proto.Int64(int64(base+40) * 1000),
	}}
	reacted := webMsg("DM2", alicePN, true, "", base+20, text("see you"))
	reacted.Status = waWeb.WebMessageInfo_READ.Enum()
	reacted.Reactions = []*waWeb.Reaction{{
		Key: &waCommon.MessageKey{ID: proto.String("REACT1"), RemoteJID: proto.String(alicePN)}, Text: proto.String("👍"),
		SenderTimestampMS: proto.Int64(int64(base+25) * 1000),
	}}
	revoked := webMsg("DM4", alicePN, false, "", base+50, nil)
	revoked.MessageStubType = waWeb.WebMessageInfo_REVOKE.Enum()
	stub := webMsg("G0", groupJID, false, bobLID, base+5, nil)
	stub.MessageStubType = waWeb.WebMessageInfo_GROUP_PARTICIPANT_ADD.Enum()
	edit := webMsg("EDIT1", alicePN, false, "", base+60, &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:           &waCommon.MessageKey{ID: proto.String("DM1"), RemoteJID: proto.String(alicePN)},
		EditedMessage: text("hello (edited)"),
	}})
	reply := webMsg("G2", groupJID, false, bobLID, base+35, &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("agreed @alice"),
		ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("G1"), Participant: proto.String(bobLID),
			QuotedMessage: text("first"), MentionedJID: []string{aliceLID}},
	}})
	image := webMsg("G1", groupJID, false, bobLID, base+10, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String("first"), Mimetype: proto.String("image/jpeg"), FileLength: proto.Uint64(2048),
	}})
	system := webMsg("DM5", alicePN, false, "", base+55, &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum(),
	}})
	blob := &waHistorySync.HistorySync{
		SyncType:   waHistorySync.HistorySync_RECENT.Enum(),
		ChunkOrder: proto.Uint32(2),
		Progress:   proto.Uint32(45),
		PhoneNumberToLidMappings: []*waHistorySync.PhoneNumberToLIDMapping{
			{PnJID: proto.String(bobPN), LidJID: proto.String(bobLID)},
		},
		Pushnames: []*waHistorySync.Pushname{
			{ID: proto.String(bobPN), Pushname: proto.String("Bob")},
			{ID: proto.String(aliceLID), Pushname: proto.String("Alice")},
		},
		StatusV3Messages: []*waWeb.WebMessageInfo{webMsg("ST1", "status@broadcast", false, alicePN, base, text("story"))},
		Conversations: []*waHistorySync.Conversation{
			{
				ID: proto.String(alicePN), LidJID: proto.String(aliceLID), Name: proto.String("Alice"),
				UnreadCount: proto.Uint32(2), Archived: proto.Bool(true), Pinned: proto.Uint32(1700000000),
				MuteEndTime: proto.Uint64(1_900_000_000), LastMsgTimestamp: proto.Uint64(base + 60),
				// Deliberately out of order: Convert sorts by (timestamp, msgOrderID).
				Messages: []*waHistorySync.HistorySyncMsg{
					hmsg(6, edit), hmsg(5, system), hmsg(4, revoked), hmsg(2, reacted),
					hmsg(1, webMsg("DM1", alicePN, false, "", base, text("hello"))),
					hmsg(0, webMsg("OLD1", alicePN, false, "", 1000, text("ancient"))),
				},
			},
			{
				ID: proto.String(groupJID), Name: proto.String("Project team"), Description: proto.String("Weekly"),
				Participant: []*waHistorySync.GroupParticipant{
					{UserJID: proto.String(bobPN), Rank: waHistorySync.GroupParticipant_ADMIN.Enum()},
					{UserJID: proto.String(aliceLID)},
				},
				Messages: []*waHistorySync.HistorySyncMsg{hmsg(1, stub), hmsg(2, image), hmsg(3, reply), hmsg(4, pollMsg)},
			},
			{ID: proto.String("status@broadcast"), Messages: []*waHistorySync.HistorySyncMsg{hmsg(1, webMsg("ST2", "status@broadcast", false, alicePN, base, text("x")))}},
		},
	}
	in := input(t, blob)
	in.CutoffMs = int64(base-100) * 1000
	res := Convert(in)

	if res.SyncType != domain.HistorySyncRecent || res.ChunkOrder != 2 || res.Progress == nil || *res.Progress != 45 {
		t.Fatalf("header = %s/%d/%v", res.SyncType, res.ChunkOrder, res.Progress)
	}
	if len(res.Batches) != 1 {
		t.Fatalf("batches = %d", len(res.Batches))
	}
	// Skipped: statusV3 (1), status chat (1), ancient (1), system (1), stub (1).
	if res.Skipped != 5 || res.Conversations != 2 {
		t.Fatalf("skipped=%d conversations=%d", res.Skipped, res.Conversations)
	}
	b := res.Batches[0].Payload
	if b.Part != 1 || b.Parts != 1 || b.RunID != "hsr_run" || b.AccountJID != ownJID || b.SnapshotAt != notifiedAt.UnixMilli() {
		t.Fatalf("batch header = %+v", b)
	}
	wantContacts := []apitypes.HistoryContact{
		{LID: aliceLID, PhoneJID: alicePN, PushName: "Alice"},
		{LID: bobLID, PhoneJID: bobPN, PushName: "Bob"},
	}
	if !reflect.DeepEqual(b.Contacts, wantContacts) {
		t.Fatalf("contacts = %+v", b.Contacts)
	}
	dm := b.Conversations[0]
	if dm.ChatJID != alicePN || dm.Type != "dm" || dm.LID != aliceLID || dm.PhoneJID != alicePN || dm.Name != "Alice" ||
		dm.UnreadCount == nil || *dm.UnreadCount != 2 || !dm.Archived || !dm.Pinned ||
		dm.MutedUntil != 1_900_000_000_000 || dm.LastMessageAt != int64(base+60)*1000 {
		t.Fatalf("dm meta = %+v", dm)
	}
	group := b.Conversations[1]
	wantParticipants := []apitypes.HistoryParticipant{
		{LID: bobLID, PhoneJID: bobPN, Role: "admin"},
		{LID: aliceLID, PhoneJID: alicePN, Role: "member"},
	}
	if group.Type != "group" || group.Name != "Project team" || group.Description != "Weekly" ||
		!reflect.DeepEqual(group.Participants, wantParticipants) {
		t.Fatalf("group meta = %+v", group)
	}

	type row struct{ id, kind, target, body string }
	var got []row
	for _, m := range dm.Messages {
		got = append(got, row{m.WAMessageID, m.Kind, m.TargetID, m.Body})
	}
	want := []row{
		{"DM1", domain.EventMessage, "", "hello"},
		{"DM2", domain.EventMessageFromMe, "", "see you"},
		{"REACT1", domain.EventMessageReaction, "DM2", ""},
		{"DM4", domain.EventMessageRevoked, "DM4", ""},
		{"EDIT1", domain.EventMessageEdited, "DM1", "hello (edited)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dm messages = %+v", got)
	}
	if m := dm.Messages[0]; m.SenderJID != alicePN || m.SenderLID != aliceLID || m.FromMe || m.Timestamp != int64(base)*1000 {
		t.Fatalf("inbound sender = %+v", m)
	}
	if m := dm.Messages[1]; !m.FromMe || m.Status != "read" || m.SenderJID != ownJID {
		t.Fatalf("own message = %+v", m)
	}
	if m := dm.Messages[2]; m.Reaction != "👍" || m.FromMe || m.SenderJID != alicePN {
		t.Fatalf("reaction = %+v", m)
	}

	gm := group.Messages
	if len(gm) != 4 {
		t.Fatalf("group messages = %d", len(gm))
	}
	if m := gm[0]; m.Type != "image" || !m.HasMedia || m.Media == nil || m.Media.Mimetype != "image/jpeg" ||
		m.Media.Size != 2048 || m.Body != "first" || m.SenderLID != bobLID || m.SenderJID != bobPN {
		t.Fatalf("image = %+v media=%+v", m, m.Media)
	}
	if m := gm[2]; m.QuotedMessageID != "G1" || m.QuotedBody != "first" || m.QuotedSenderLID != bobLID ||
		len(m.Mentions) != 1 || m.Kind != domain.EventMessage {
		t.Fatalf("reply = %+v", m)
	}
	if m := gm[1]; m.Type != "poll" || m.Poll == nil || m.Poll.Name != "Lunch?" || m.Kind != domain.EventMessageFromMe {
		t.Fatalf("poll = %+v", m)
	}
	if m := gm[3]; m.Kind != domain.EventPollVote || m.TargetID != "POLL1" || !reflect.DeepEqual(m.SelectedOptions, []string{"No"}) ||
		m.SenderLID != bobLID {
		t.Fatalf("vote = %+v", m)
	}
}

func TestConvertIsDeterministic(t *testing.T) {
	blob := bigBlob(3, 150, 40)
	a, b := Convert(input(t, blob)), Convert(input(t, blob))
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same input produced different results")
	}
	other := input(t, blob)
	other.NotificationID = "NOTIF2"
	c := Convert(other)
	seen := map[string]bool{}
	for i, batch := range a.Batches {
		if seen[batch.EventID] {
			t.Fatalf("duplicate event id %s", batch.EventID)
		}
		seen[batch.EventID] = true
		if batch.EventID == c.Batches[i].EventID {
			t.Fatal("event ids do not depend on the notification id")
		}
		parsed := ulid.MustParse(strings.TrimPrefix(batch.EventID, domain.PrefixEvent))
		if int64(parsed.Time()) != notifiedAt.UnixMilli() {
			t.Fatalf("event id time = %d", parsed.Time())
		}
	}
	first, again, other2 := RunID(notifiedAt, "s", "n"), RunID(notifiedAt, "s", "n"), RunID(notifiedAt, "s", "m")
	if first != again || first == other2 || !strings.HasPrefix(first, "hsr_") {
		t.Fatal("run ids are not deterministic per notification")
	}
}

// bigBlob builds chats × perChat text messages with bodies of bodyLen bytes.
func bigBlob(chats, perChat, bodyLen int) *waHistorySync.HistorySync {
	base := uint64(notifiedAt.Unix()) - 86400
	blob := &waHistorySync.HistorySync{SyncType: waHistorySync.HistorySync_INITIAL_BOOTSTRAP.Enum()}
	for c := 0; c < chats; c++ {
		chat := fmt.Sprintf("1555100%04d@s.whatsapp.net", c)
		conv := &waHistorySync.Conversation{ID: proto.String(chat), Name: proto.String(fmt.Sprintf("Chat %d", c))}
		for i := 0; i < perChat; i++ {
			body := strings.Repeat("x", bodyLen)
			conv.Messages = append(conv.Messages, hmsg(uint64(i), webMsg(fmt.Sprintf("C%dM%d", c, i), chat, i%2 == 0, "", base+uint64(i), text(body))))
		}
		blob.Conversations = append(blob.Conversations, conv)
	}
	return blob
}

func checkBatches(t *testing.T, res Result) (messages int) {
	t.Helper()
	for i, batch := range res.Batches {
		p := batch.Payload
		if p.Part != i+1 || p.Parts != len(res.Batches) {
			t.Fatalf("batch %d part %d/%d", i, p.Part, p.Parts)
		}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, c := range p.Conversations {
			count += len(c.Messages)
			for j := 1; j < len(c.Messages); j++ {
				if c.Messages[j].Timestamp < c.Messages[j-1].Timestamp {
					t.Fatalf("batch %d chat %s not oldest first", i, c.ChatJID)
				}
			}
		}
		if count > MaxBatchMessages {
			t.Fatalf("batch %d has %d messages", i, count)
		}
		if count > 1 && len(raw) > MaxBatchBytes {
			t.Fatalf("batch %d is %d bytes", i, len(raw))
		}
		messages += count
	}
	return messages
}

func TestConvertSplitsByMessageCount(t *testing.T) {
	res := Convert(input(t, bigBlob(1, 450, 10)))
	if len(res.Batches) != 3 {
		t.Fatalf("batches = %d", len(res.Batches))
	}
	if n := checkBatches(t, res); n != 450 || res.Messages != 450 {
		t.Fatalf("messages = %d/%d", n, res.Messages)
	}
	for _, b := range res.Batches {
		// Conversation metadata repeats in every part that carries its messages.
		if b.Payload.Conversations[0].Name != "Chat 0" {
			t.Fatal("conversation metadata missing from a part")
		}
	}
	if got := len(res.Batches[2].Payload.Conversations[0].Messages); got != 50 {
		t.Fatalf("last batch = %d messages", got)
	}
}

func TestConvertSplitsByBytes(t *testing.T) {
	res := Convert(input(t, bigBlob(4, 60, 2000)))
	if n := checkBatches(t, res); n != 240 {
		t.Fatalf("messages = %d", n)
	}
	if len(res.Batches) < 5 {
		t.Fatalf("expected byte-bounded batches, got %d", len(res.Batches))
	}
}

func TestConvertOversizedMessages(t *testing.T) {
	base := uint64(notifiedAt.Unix()) - 60
	big := strings.Repeat("b", 200*1024)
	huge := strings.Repeat("h", 2*1024*1024)
	blob := &waHistorySync.HistorySync{SyncType: waHistorySync.HistorySync_RECENT.Enum(), Conversations: []*waHistorySync.Conversation{{
		ID: proto.String(alicePN),
		Messages: []*waHistorySync.HistorySyncMsg{
			hmsg(1, webMsg("A", alicePN, false, "", base, text("small before"))),
			hmsg(2, webMsg("B", alicePN, false, "", base+1, text(big))),
			hmsg(3, webMsg("C", alicePN, false, "", base+2, &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String(huge), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("A"), QuotedMessage: text("q")},
			}})),
			hmsg(4, webMsg("D", alicePN, false, "", base+3, text("small after"))),
		},
	}}}
	res := Convert(input(t, blob))
	var ids [][]string
	for _, b := range res.Batches {
		var batch []string
		for _, m := range b.Payload.Conversations[0].Messages {
			batch = append(batch, m.WAMessageID)
			raw, _ := json.Marshal(m)
			if len(raw) > MaxMessageBytes {
				t.Fatalf("message %s is %d bytes", m.WAMessageID, len(raw))
			}
			if m.WAMessageID == "C" && (!m.Truncated || len(m.Body) >= len(huge)) {
				t.Fatalf("huge message not truncated: truncated=%v len=%d", m.Truncated, len(m.Body))
			}
			if m.WAMessageID == "B" && (m.Truncated || m.Body != big) {
				t.Fatal("200 KiB message must travel whole")
			}
		}
		ids = append(ids, batch)
	}
	want := [][]string{{"A"}, {"B"}, {"C"}, {"D"}}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("batches = %v", ids)
	}
}

func TestConvertSplitsContacts(t *testing.T) {
	blob := &waHistorySync.HistorySync{SyncType: waHistorySync.HistorySync_PUSH_NAME.Enum()}
	for i := 0; i < 2000; i++ {
		blob.Pushnames = append(blob.Pushnames, &waHistorySync.Pushname{
			ID: proto.String(fmt.Sprintf("1555200%04d@s.whatsapp.net", i)), Pushname: proto.String(fmt.Sprintf("Name %d", i)),
		})
	}
	res := Convert(input(t, blob))
	total := 0
	for _, b := range res.Batches {
		raw, _ := json.Marshal(b.Payload)
		if len(raw) > MaxBatchBytes {
			t.Fatalf("contact batch is %d bytes", len(raw))
		}
		total += len(b.Payload.Contacts)
	}
	if total != 2000 || len(res.Batches) < 2 || res.SyncType != domain.HistorySyncPushName {
		t.Fatalf("contacts=%d batches=%d type=%s", total, len(res.Batches), res.SyncType)
	}
}

func TestConvertEmptyBlob(t *testing.T) {
	res := Convert(input(t, &waHistorySync.HistorySync{SyncType: waHistorySync.HistorySync_NON_BLOCKING_DATA.Enum()}))
	if len(res.Batches) != 0 || res.Progress != nil || res.SyncType != domain.HistorySyncNonBlockingData {
		t.Fatalf("result = %+v", res)
	}
}
