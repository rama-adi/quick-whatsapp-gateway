package fakewhatsapp

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// History sync simulation. After pairing (when the number's config enables
// it) or on request, the fake phone sends what a real phone sends to a new
// linked device: an INITIAL_BOOTSTRAP notification with a zlib-compressed
// inline payload built from the number's transcripts, followed by RECENT
// notifications whose blobs the client downloads from the fake media server.
// The last RECENT chunk reports 100% progress.

// Limits keep a panel typo from building a multi-gigabyte blob.
const (
	maxHistoryChats         = 500
	maxHistoryMessages      = 5000
	historyChatsPerChunk    = 10
	historyDownloadNotFound = http.StatusNotFound
)

// HistoryRequest asks the fake phone to send a history sync.
type HistoryRequest struct {
	// Chats adds this many synthetic direct chats to the recent history.
	Chats int `json:"chats"`
	// Messages is the number of messages in each synthetic chat.
	Messages int `json:"messages"`
	// Force404 makes every downloadable chunk answer 404, as when WhatsApp
	// already deleted the blob.
	Force404 bool `json:"force_404"`
}

// HistoryResponse reports how many notifications reached gateway sessions.
type HistoryResponse struct {
	Notifications int `json:"notifications"`
	Delivered     int `json:"delivered"`
}

func validateHistory(req HistoryRequest) error {
	if req.Chats < 0 || req.Chats > maxHistoryChats {
		return fmt.Errorf("history chats must be between 0 and %d", maxHistoryChats)
	}
	if req.Messages < 0 || req.Messages > maxHistoryMessages {
		return fmt.Errorf("history messages per chat must be between 0 and %d", maxHistoryMessages)
	}
	return nil
}

func (s *Server) historyRoute(w http.ResponseWriter, r *http.Request, number string) {
	if err := validateNumber(number); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req HistoryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateHistory(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	result, err := s.queueHistory(number, req)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// queueHistory sends a history sync to every connected session of number.
// The caller holds s.mu.
func (s *Server) queueHistory(number string, req HistoryRequest) (HistoryResponse, error) {
	var result HistoryResponse
	chunks := s.buildHistory(number, req, time.Now())
	own := number + "@s.whatsapp.net"
	for _, se := range s.sessions {
		if se.number != number || !se.connected || !se.paired {
			continue
		}
		for i, blob := range chunks {
			raw, err := proto.Marshal(blob)
			if err != nil {
				return result, err
			}
			var compressed bytes.Buffer
			zw := zlib.NewWriter(&compressed)
			if _, err := zw.Write(raw); err != nil {
				return result, err
			}
			if err := zw.Close(); err != nil {
				return result, err
			}
			s.inboundSequence++
			id := fmt.Sprintf("FAKE_HIST_%d", s.inboundSequence)
			notif := &waE2E.HistorySyncNotification{
				SyncType:   waE2E.HistorySyncType(blob.GetSyncType()).Enum(),
				ChunkOrder: proto.Uint32(blob.GetChunkOrder()),
				Progress:   blob.Progress,
				FileLength: proto.Uint64(uint64(compressed.Len())),
			}
			if i == 0 {
				notif.InitialHistBootstrapInlinePayload = compressed.Bytes()
			} else {
				sum := sha256.Sum256(compressed.Bytes())
				key := make([]byte, 32)
				_, _ = rand.Read(key)
				path := "/history/" + id
				s.media[path] = compressed.Bytes()
				if req.Force404 {
					s.expiredHistory[path] = true
				}
				notif.DirectPath, notif.FileEncSHA256, notif.FileSHA256, notif.MediaKey = proto.String(path), sum[:], sum[:], key
			}
			message, err := protojson.Marshal(&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
				Type:                    waE2E.ProtocolMessage_HISTORY_SYNC_NOTIFICATION.Enum(),
				HistorySyncNotification: notif,
			}})
			if err != nil {
				return result, err
			}
			s.appendEvent(se, Event{Kind: "message", ID: id, Chat: own, Sender: own, FromMe: true,
				Timestamp: time.Now().UnixMilli(), Message: message})
			result.Delivered++
		}
		result.Notifications = len(chunks)
	}
	return result, nil
}

// buildHistory returns the bootstrap blob (the number's transcripts, or a
// small default conversation) followed by RECENT blobs with the synthetic
// chats. The caller holds s.mu.
func (s *Server) buildHistory(number string, req HistoryRequest, now time.Time) []*waHistorySync.HistorySync {
	bootstrap := &waHistorySync.HistorySync{
		SyncType:   waHistorySync.HistorySync_INITIAL_BOOTSTRAP.Enum(),
		ChunkOrder: proto.Uint32(1),
	}
	transcripts := s.transcripts[number]
	chats := make([]string, 0, len(transcripts))
	for chat := range transcripts {
		chats = append(chats, chat)
	}
	sort.Strings(chats)
	for _, chat := range chats {
		bootstrap.Conversations = append(bootstrap.Conversations, s.transcriptConversation(number, chat, transcripts[chat]))
	}
	if len(bootstrap.Conversations) == 0 {
		bootstrap.Conversations = defaultHistory(now)
		bootstrap.Pushnames = []*waHistorySync.Pushname{
			{ID: proto.String("555000002@s.whatsapp.net"), Pushname: proto.String("Alex Example")},
			{ID: proto.String("555000003@s.whatsapp.net"), Pushname: proto.String("Bea Example")},
		}
	}
	for jid, name := range s.contactNames[number] {
		bootstrap.Pushnames = append(bootstrap.Pushnames, &waHistorySync.Pushname{ID: proto.String(jid), Pushname: proto.String(name)})
	}
	sort.Slice(bootstrap.Pushnames, func(i, j int) bool { return bootstrap.Pushnames[i].GetID() < bootstrap.Pushnames[j].GetID() })

	out := []*waHistorySync.HistorySync{bootstrap}
	var recent []*waHistorySync.Conversation
	if req.Messages > 0 {
		for c := 0; c < req.Chats; c++ {
			recent = append(recent, syntheticConversation(number, c, req.Messages, now))
		}
	}
	chunkCount := max((len(recent)+historyChatsPerChunk-1)/historyChatsPerChunk, 1)
	for i := 0; i < chunkCount; i++ {
		end := min((i+1)*historyChatsPerChunk, len(recent))
		start := min(i*historyChatsPerChunk, end)
		out = append(out, &waHistorySync.HistorySync{
			SyncType:      waHistorySync.HistorySync_RECENT.Enum(),
			ChunkOrder:    proto.Uint32(uint32(i + 1)),
			Progress:      proto.Uint32(uint32((i + 1) * 100 / chunkCount)),
			Conversations: recent[start:end],
		})
	}
	return out
}

func (s *Server) transcriptConversation(number, chat string, messages []actorMessage) *waHistorySync.Conversation {
	conv := &waHistorySync.Conversation{ID: proto.String(chat)}
	isGroup := strings.HasSuffix(chat, "@g.us")
	var member string
	if group := s.groups[chat]; group != nil {
		conv.Name = proto.String(group.Name)
		for _, p := range group.Participants {
			conv.Participant = append(conv.Participant, &waHistorySync.GroupParticipant{UserJID: proto.String(p.JID.String())})
			if member == "" && p.PhoneNumber.User != number && p.JID.User != number {
				member = p.JID.String()
			}
		}
	} else if name := s.contactNames[number][chat]; name != "" {
		conv.Name = proto.String(name)
	}
	for i, m := range messages {
		wm := historyMessage(chat, m.ID, m.FromMe, m.Timestamp/1000, transcriptText(m))
		if isGroup && !m.FromMe {
			if member == "" {
				continue // no known sender: a real phone would not have it either
			}
			wm.Participant = proto.String(member)
		}
		if m.FromMe {
			wm.Status = waWeb.WebMessageInfo_SERVER_ACK.Enum()
		}
		conv.Messages = append(conv.Messages, &waHistorySync.HistorySyncMsg{Message: wm, MsgOrderID: proto.Uint64(uint64(i + 1))})
		conv.LastMsgTimestamp = proto.Uint64(uint64(m.Timestamp / 1000))
	}
	return conv
}

func transcriptText(m actorMessage) string {
	if m.Text != "" {
		return m.Text
	}
	if m.Kind == "" {
		return "message"
	}
	return m.Kind + " message"
}

func defaultHistory(now time.Time) []*waHistorySync.Conversation {
	start := now.Add(-2 * time.Hour).Unix()
	alex := &waHistorySync.Conversation{ID: proto.String("555000002@s.whatsapp.net"), Name: proto.String("Alex Example"),
		UnreadCount: proto.Uint32(1)}
	bea := &waHistorySync.Conversation{ID: proto.String("555000003@s.whatsapp.net"), Name: proto.String("Bea Example")}
	lines := []struct {
		conv   *waHistorySync.Conversation
		fromMe bool
		text   string
	}{
		{alex, false, "Hi! Did the history import work?"},
		{alex, true, "Checking the dashboard now"},
		{bea, true, "Lunch tomorrow?"},
		{bea, false, "Sure, 12:30"},
		{alex, false, "Great, see you there"},
	}
	for i, line := range lines {
		id := fmt.Sprintf("FAKE_HISTORY_SEED_%d", i+1)
		wm := historyMessage(line.conv.GetID(), id, line.fromMe, start+int64(i*60), line.text)
		if line.fromMe {
			wm.Status = waWeb.WebMessageInfo_READ.Enum()
		}
		line.conv.Messages = append(line.conv.Messages, &waHistorySync.HistorySyncMsg{Message: wm, MsgOrderID: proto.Uint64(uint64(i + 1))})
	}
	return []*waHistorySync.Conversation{alex, bea}
}

// syntheticConversation is chat c of a large history: messages spread over
// the last seven days, every third one sent by the phone owner.
func syntheticConversation(number string, c, messages int, now time.Time) *waHistorySync.Conversation {
	chat := fmt.Sprintf("555800%05d@s.whatsapp.net", c)
	conv := &waHistorySync.Conversation{ID: proto.String(chat), Name: proto.String(fmt.Sprintf("History contact %d", c+1))}
	span := int64(7 * 24 * 3600)
	start := now.Unix() - span
	for j := 0; j < messages; j++ {
		ts := start + int64(j)*span/int64(messages) + int64(c)
		id := fmt.Sprintf("FAKE_HISTORY_%s_%d_%d", number, c, j)
		wm := historyMessage(chat, id, j%3 == 0, ts, fmt.Sprintf("History message %d in chat %d", j+1, c+1))
		conv.Messages = append(conv.Messages, &waHistorySync.HistorySyncMsg{Message: wm, MsgOrderID: proto.Uint64(uint64(j + 1))})
	}
	conv.LastMsgTimestamp = proto.Uint64(uint64(now.Unix()))
	return conv
}

func historyMessage(chat, id string, fromMe bool, tsSec int64, text string) *waWeb.WebMessageInfo {
	return &waWeb.WebMessageInfo{
		Key:              &waCommon.MessageKey{RemoteJID: proto.String(chat), FromMe: proto.Bool(fromMe), ID: proto.String(id)},
		MessageTimestamp: proto.Uint64(uint64(max(tsSec, 0))),
		Message:          &waE2E.Message{Conversation: proto.String(text)},
	}
}

// performHistory answers the history operations of the fake client. The
// caller holds s.mu.
func (s *Server) performHistory(req OperationRequest) (OperationResponse, bool) {
	switch req.Kind {
	case "history_download":
		data, ok := s.media[req.To]
		if !ok || s.expiredHistory[req.To] || !strings.HasPrefix(req.To, "/history/") {
			return OperationResponse{Status: historyDownloadNotFound, Error: "history blob not found"}, true
		}
		encoded, _ := json.Marshal(data) // []byte encodes as base64
		return OperationResponse{Result: encoded}, true
	case "history_receipt", "history_server_error":
		return OperationResponse{}, true
	case "delete_media":
		if strings.HasPrefix(req.To, "/history/") {
			delete(s.media, req.To)
			delete(s.expiredHistory, req.To)
		}
		return OperationResponse{}, true
	}
	return OperationResponse{}, false
}
