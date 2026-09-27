package fakewhatsapp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/encoding/protojson"
)

type actorSend struct {
	Chat         string   `json:"chat"`
	From         string   `json:"from"`
	FromName     string   `json:"from_name"`
	QuoteID      string   `json:"quote_id"`
	Kind         string   `json:"kind"`
	Text         string   `json:"text"`
	Filename     string   `json:"filename"`
	MimeType     string   `json:"mime_type"`
	DataBase64   string   `json:"data_base64"`
	Latitude     float64  `json:"latitude"`
	Longitude    float64  `json:"longitude"`
	ContactName  string   `json:"contact_name"`
	ContactPhone string   `json:"contact_phone"`
	PollOptions  []string `json:"poll_options"`
	Buttons      []string `json:"buttons"`
	ListRows     []string `json:"list_rows"`
}

type actorMessage struct {
	ID        string `json:"id"`
	FromMe    bool   `json:"from_me"`
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	Timestamp int64  `json:"timestamp"`
	Status    string `json:"status,omitempty"`
	MediaURL  string `json:"media_url,omitempty"`
}
type actorChat struct {
	JID      string         `json:"jid"`
	Name     string         `json:"name"`
	IsGroup  bool           `json:"is_group"`
	Messages []actorMessage `json:"messages"`
}

func (s *Server) actorRoute(w http.ResponseWriter, r *http.Request, parts []string) bool {
	if len(parts) < 4 || parts[0] != "v1" || parts[1] != "numbers" {
		return false
	}
	number := parts[2]
	if err := validateNumber(number); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}
	switch {
	case len(parts) == 4 && parts[3] == "send" && r.Method == http.MethodPost:
		s.actorSend(w, r, number)
	case len(parts) == 4 && parts[3] == "chats" && r.Method == http.MethodGet:
		s.actorChats(w, number)
	case len(parts) == 4 && parts[3] == "media" && r.Method == http.MethodPost:
		s.actorUpload(w, r, number)
	case len(parts) == 5 && parts[3] == "media" && r.Method == http.MethodGet:
		s.actorMedia(w, number, parts[4])
	case len(parts) == 4 && parts[3] == "effects" && r.Method == http.MethodPost:
		s.actorEffect(w, r, number)
	case len(parts) == 4 && parts[3] == "groups" && r.Method == http.MethodGet:
		s.actorGroups(w, number)
	case len(parts) == 4 && parts[3] == "groups" && r.Method == http.MethodPost:
		s.actorCreateGroup(w, r, number)
	case len(parts) == 6 && parts[3] == "groups" && parts[5] == "members" && r.Method == http.MethodPost:
		s.actorGroupMember(w, r, number, parts[4])
	default:
		return false
	}
	return true
}

func (s *Server) actorSend(w http.ResponseWriter, r *http.Request, number string) {
	var req actorSend
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Chat == "" {
		http.Error(w, "chat required", http.StatusBadRequest)
		return
	}
	if _, err := types.ParseJID(req.Chat); err != nil {
		http.Error(w, "invalid chat JID: "+err.Error(), http.StatusBadRequest)
		return
	}
	message, mediaPath, err := s.actorMessage(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	raw, err := protojson.Marshal(message)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	sender := req.From
	if sender == "" {
		sender = req.Chat
		if strings.HasSuffix(req.Chat, "@g.us") {
			group := s.groups[req.Chat]
			if group != nil {
				for _, participant := range group.Participants {
					if participant.PhoneNumber.User != number {
						sender = participant.JID.String()
						break
					}
				}
			}
		}
	}
	if _, err := types.ParseJID(sender); err != nil {
		s.mu.Unlock()
		http.Error(w, "invalid sender JID: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.inboundSequence++
	id := fmt.Sprintf("FAKE_IN_%d", s.inboundSequence)
	delivered := 0
	for _, se := range s.sessions {
		if se.number != number || !se.connected {
			continue
		}
		s.appendEvent(se, Event{Kind: "message", ID: id, Chat: req.Chat, Sender: sender, PushName: req.FromName, Timestamp: time.Now().UnixMilli(), Message: raw})
		delivered++
	}
	if s.transcripts[number] == nil {
		s.transcripts[number] = map[string][]actorMessage{}
	}
	if req.FromName != "" {
		if s.contactNames[number] == nil {
			s.contactNames[number] = map[string]string{}
		}
		s.contactNames[number][sender] = req.FromName
	}
	s.transcripts[number][req.Chat] = append(s.transcripts[number][req.Chat], actorMessage{ID: id, Kind: req.Kind, Text: req.Text, Timestamp: time.Now().UnixMilli(), MediaURL: actorMediaURL(number, mediaPath)})
	s.bumpState()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "delivered": delivered})
}

func (s *Server) actorMessage(req actorSend) (*waE2E.Message, string, error) {
	var payload map[string]any
	var mediaPath string
	switch req.Kind {
	case "", "text":
		if req.QuoteID == "" {
			payload = map[string]any{"conversation": req.Text}
		} else {
			payload = map[string]any{"extendedTextMessage": map[string]any{"text": req.Text, "contextInfo": map[string]any{"stanzaID": req.QuoteID, "remoteJID": req.Chat}}}
		}
	case "image", "video", "audio", "document", "sticker":
		data, err := base64.StdEncoding.DecodeString(req.DataBase64)
		if err != nil {
			return nil, "", fmt.Errorf("invalid media: %w", err)
		}
		if len(data) == 0 {
			return nil, "", fmt.Errorf("media data required")
		}
		upload := hashUpload(data)
		mediaPath = upload.DirectPath
		s.mu.Lock()
		s.media[mediaPath] = append([]byte{}, data...)
		s.mu.Unlock()
		base := map[string]any{"URL": upload.URL, "directPath": upload.DirectPath, "mimetype": req.MimeType, "fileLength": fmt.Sprintf("%d", upload.FileLength), "fileSHA256": upload.FileSHA256, "fileEncSHA256": upload.FileEncSHA256, "mediaKey": upload.MediaKey}
		switch req.Kind {
		case "image":
			base["caption"] = req.Text
			payload = map[string]any{"imageMessage": base}
		case "video":
			base["caption"] = req.Text
			payload = map[string]any{"videoMessage": base}
		case "audio":
			payload = map[string]any{"audioMessage": base}
		case "sticker":
			payload = map[string]any{"stickerMessage": base}
		case "document":
			base["caption"] = req.Text
			base["fileName"] = req.Filename
			payload = map[string]any{"documentMessage": base}
		}
	case "location":
		payload = map[string]any{"locationMessage": map[string]any{"degreesLatitude": req.Latitude, "degreesLongitude": req.Longitude, "name": req.Text}}
	case "contact":
		vcard := "BEGIN:VCARD\nVERSION:3.0\nFN:" + req.ContactName + "\nTEL:" + req.ContactPhone + "\nEND:VCARD"
		payload = map[string]any{"contactMessage": map[string]any{"displayName": req.ContactName, "vcard": vcard}}
	case "poll":
		options := []map[string]string{}
		for _, value := range req.PollOptions {
			options = append(options, map[string]string{"optionName": value})
		}
		payload = map[string]any{"pollCreationMessage": map[string]any{"name": req.Text, "options": options, "selectableOptionsCount": 1}}
	case "buttons":
		buttons := []map[string]any{}
		for i, value := range req.Buttons {
			buttons = append(buttons, map[string]any{"buttonID": fmt.Sprintf("fake-%d", i+1), "buttonText": map[string]string{"displayText": value}, "type": 1})
		}
		payload = map[string]any{"buttonsMessage": map[string]any{"contentText": req.Text, "buttons": buttons, "headerType": 1}}
	case "list":
		rows := []map[string]any{}
		for i, value := range req.ListRows {
			rows = append(rows, map[string]any{"rowID": fmt.Sprintf("fake-%d", i+1), "title": value})
		}
		payload = map[string]any{"listMessage": map[string]any{"title": req.Text, "buttonText": "Choose", "sections": []map[string]any{{"title": "Options", "rows": rows}}}}
	default:
		return nil, "", fmt.Errorf("unsupported message kind %q", req.Kind)
	}
	raw, _ := json.Marshal(payload)
	message := &waE2E.Message{}
	if err := protojson.Unmarshal(raw, message); err != nil {
		return nil, "", fmt.Errorf("unsupported %s payload: %w", req.Kind, err)
	}
	return message, mediaPath, nil
}

func (s *Server) actorChats(w http.ResponseWriter, number string) {
	s.mu.Lock()
	chats := []actorChat{}
	for jid, messages := range s.transcripts[number] {
		name := jid
		if known := s.contactNames[number][jid]; known != "" {
			name = known
		}
		if group := s.groups[jid]; group != nil {
			name = group.Name
		}
		chats = append(chats, actorChat{JID: jid, Name: name, IsGroup: strings.HasSuffix(jid, "@g.us"), Messages: append([]actorMessage{}, messages...)})
	}
	s.mu.Unlock()
	sort.Slice(chats, func(i, j int) bool { return chats[i].JID < chats[j].JID })
	writeJSON(w, http.StatusOK, map[string]any{"chats": chats})
}

func (s *Server) actorMedia(w http.ResponseWriter, number, id string) {
	path := "/media/" + id
	s.mu.Lock()
	data, ok := s.media[path]
	copy := append([]byte{}, data...)
	s.mu.Unlock()
	if !ok {
		http.Error(w, "media not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(copy))
	_, _ = w.Write(copy)
}

func (s *Server) actorEffect(w http.ResponseWriter, r *http.Request, number string) {
	var req struct {
		Chat      string `json:"chat"`
		MessageID string `json:"message_id"`
		Kind      string `json:"kind"`
		Value     string `json:"value"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Chat == "" || req.MessageID == "" {
		http.Error(w, "chat and message_id required", http.StatusBadRequest)
		return
	}
	var event Event
	switch req.Kind {
	case "delivered", "read", "played":
		data, _ := json.Marshal(map[string]any{"type": req.Kind, "message_ids": []string{req.MessageID}, "chat": req.Chat, "sender": req.Chat})
		event = Event{Kind: "receipt", Data: data, Chat: req.Chat, Sender: req.Chat, ID: req.MessageID}
	case "reaction", "edit", "delete":
		payload := map[string]any{}
		if req.Kind == "reaction" {
			payload["reactionMessage"] = map[string]any{"key": map[string]any{"remoteJID": req.Chat, "ID": req.MessageID, "fromMe": false}, "text": req.Value}
		} else {
			kind := "REVOKE"
			protocol := map[string]any{"key": map[string]any{"remoteJID": req.Chat, "ID": req.MessageID, "fromMe": false}}
			if req.Kind == "edit" {
				kind = "MESSAGE_EDIT"
				protocol["editedMessage"] = map[string]any{"conversation": req.Value}
			}
			protocol["type"] = kind
			payload["protocolMessage"] = protocol
		}
		raw, _ := json.Marshal(payload)
		message := &waE2E.Message{}
		if err := protojson.Unmarshal(raw, message); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		encoded, _ := protojson.Marshal(message)
		event = Event{Kind: "message", ID: fmt.Sprintf("FAKE_EFFECT_%d", time.Now().UnixNano()), Chat: req.Chat, Sender: req.Chat, Message: encoded}
	default:
		http.Error(w, "unsupported effect kind", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	for i := range s.transcripts[number][req.Chat] {
		message := &s.transcripts[number][req.Chat][i]
		if message.ID != req.MessageID {
			continue
		}
		switch req.Kind {
		case "delivered", "read", "played":
			message.Status = req.Kind
		case "edit":
			message.Text = req.Value
		case "delete":
			message.Kind = "deleted"
			message.Text = ""
		}
	}
	s.bumpState()
	count := 0
	for _, se := range s.sessions {
		if se.number == number && se.connected {
			s.appendEvent(se, event)
			count++
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]int{"delivered": count})
}

func (s *Server) actorGroups(w http.ResponseWriter, number string) {
	s.mu.Lock()
	groups := []map[string]any{}
	for jid, group := range s.groups {
		if !s.groupAccessible(jid, number) {
			continue
		}
		participants := []string{}
		for _, participant := range group.Participants {
			participants = append(participants, participant.PhoneNumber.String())
		}
		groups = append(groups, map[string]any{"jid": jid, "name": group.Name, "participants": participants})
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (s *Server) actorCreateGroup(w http.ResponseWriter, r *http.Request, number string) {
	var req struct {
		Name         string   `json:"name"`
		Participants []string `json:"participants"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		http.Error(w, "group name required", http.StatusBadRequest)
		return
	}
	members := []types.GroupParticipant{}
	for _, phone := range req.Participants {
		if err := validateNumber(phone); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		members = append(members, fakeParticipant(phone))
	}
	s.mu.Lock()
	owner := fakeParticipant(number)
	owner.IsSuperAdmin = true
	members = append([]types.GroupParticipant{owner}, members...)
	s.groupSequence++
	jid := types.NewJID(fmt.Sprintf("120363%d", s.groupSequence), types.GroupServer)
	group := &types.GroupInfo{JID: jid, GroupName: types.GroupName{Name: req.Name}, Participants: members}
	s.groups[jid.String()] = group
	s.groupOwners[jid.String()] = number
	s.grantGroup(jid.String(), number)
	s.bumpState()
	delivered := 0
	for _, se := range s.sessions {
		if se.number == number && se.connected {
			raw, _ := json.Marshal(group)
			s.appendEvent(se, Event{Kind: "joined_group", Data: raw})
			delivered++
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"jid": jid.String(), "name": req.Name, "participants": req.Participants, "delivered": delivered})
}

func (s *Server) actorGroupMember(w http.ResponseWriter, r *http.Request, number, jid string) {
	var req struct {
		Phone  string `json:"phone"`
		Action string `json:"action"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateNumber(req.Phone); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	group := s.groups[jid]
	if group == nil || !s.groupAccessible(jid, number) {
		s.mu.Unlock()
		http.Error(w, "group not found", http.StatusNotFound)
		return
	}
	member := types.NewJID(req.Phone, types.DefaultUserServer)
	index := groupParticipantIndex(group.Participants, member)
	switch req.Action {
	case "add":
		if index < 0 {
			group.Participants = append(group.Participants, fakeParticipant(req.Phone))
		}
	case "remove":
		if index >= 0 {
			group.Participants = append(group.Participants[:index], group.Participants[index+1:]...)
		}
	case "promote":
		if index >= 0 {
			group.Participants[index].IsAdmin = true
		}
	case "demote":
		if index >= 0 {
			group.Participants[index].IsAdmin = false
		}
	default:
		s.mu.Unlock()
		http.Error(w, "unsupported member action", http.StatusBadRequest)
		return
	}
	s.bumpState()
	for _, se := range s.sessions {
		if se.number == number && se.connected {
			raw, _ := json.Marshal(group)
			s.appendEvent(se, Event{Kind: "group_info", Data: raw})
		}
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func actorMediaURL(number, path string) string {
	if path == "" {
		return ""
	}
	return "/v1/numbers/" + number + "/media/" + strings.TrimPrefix(path, "/media/")
}

func (s *Server) actorUpload(w http.ResponseWriter, r *http.Request, number string) {
	var req struct {
		DataBase64 string `json:"data_base64"`
		MimeType   string `json:"mime_type"`
		Filename   string `json:"filename"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.DataBase64)
	if err != nil || len(data) == 0 {
		http.Error(w, "valid nonempty data_base64 required", http.StatusBadRequest)
		return
	}
	upload := hashUpload(data)
	s.mu.Lock()
	s.media[upload.DirectPath] = append([]byte{}, data...)
	s.bumpState()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, upload)
}
