package fakewhatsapp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/encoding/protojson"
)

func (s *Server) operation(w http.ResponseWriter, r *http.Request, key string) {
	var req OperationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Kind == "" {
		http.Error(w, "operation kind required", http.StatusBadRequest)
		return
	}
	if req.Kind == "send" && len(req.Message) == 0 {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}
	if len(req.Message) > 0 {
		if err := protojson.Unmarshal(req.Message, &waE2E.Message{}); err != nil {
			http.Error(w, "invalid WhatsApp message: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	s.mu.Lock()
	se := s.getSession(key)
	cfg := s.config(se.number)
	if req.Kind == "send" {
		s.attempts++
		se.attempts++
		s.attemptTrace = append(s.attemptTrace, Attempt{Session: key, Number: se.number, ID: req.ID, To: req.To, Outcome: "accepted"})
	}
	attemptIndex := len(s.attemptTrace) - 1
	s.operations = append(s.operations, Operation{Session: key, Number: se.number, Kind: req.Kind, To: req.To, Payload: req.Payload})
	s.bumpState()
	if !se.connected && req.Kind != "upload" {
		if req.Kind == "send" {
			s.attemptTrace[attemptIndex].Outcome = "disconnected_before_commit"
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, OperationResponse{Error: "fake WhatsApp device is disconnected"})
		return
	}
	if req.Kind == "send" && cfg.Scenario == "block_send" {
		if se.release == nil {
			se.release = make(chan struct{})
		}
		wait := se.release
		se.blocked++
		s.blocked++
		s.bumpState()
		s.mu.Unlock()
		select {
		case <-wait:
		case <-r.Context().Done():
			s.mu.Lock()
			se.blocked--
			s.blocked--
			s.bumpState()
			s.attemptTrace[attemptIndex].Outcome = "cancelled_before_commit"
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		se.blocked--
		s.blocked--
		s.bumpState()
		cfg = s.config(se.number)
		if !se.connected {
			s.attemptTrace[attemptIndex].Outcome = "disconnected_before_commit"
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, OperationResponse{Error: "simulated WhatsApp disconnect before acknowledgement"})
			return
		}
	}
	resp := s.perform(se, req, cfg)
	s.bumpState()
	if req.Kind == "send" {
		last := &s.attemptTrace[attemptIndex]
		switch {
		case cfg.Scenario == "commit_drop_ack" && resp.Error != "":
			last.Outcome = "committed_ack_lost"
		case resp.Error != "":
			last.Outcome = "rejected_before_commit"
		default:
			last.Outcome = "committed_acknowledged"
		}
		if last.ID == "" {
			last.ID = resp.ID
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) perform(se *session, req OperationRequest, cfg Config) OperationResponse {
	if req.Kind == "upload" {
		if cfg.Scenario == "upload_error" {
			message := cfg.UploadError
			if message == "" {
				message = "simulated WhatsApp upload failure"
			}
			return OperationResponse{Error: message}
		}
		data, err := base64.StdEncoding.DecodeString(req.DataBase64)
		if err != nil {
			return OperationResponse{Error: "invalid upload data: " + err.Error()}
		}
		upload := hashUpload(data)
		s.media[upload.DirectPath] = append([]byte{}, data...)
		return OperationResponse{Upload: upload}
	}
	if cfg.Scenario == "send_error" {
		message := cfg.SendError
		if message == "" {
			message = "simulated WhatsApp disconnect before acknowledgement"
		}
		return OperationResponse{Error: message}
	}
	if req.Kind == "send" && cfg.Scenario == "server_405" {
		return OperationResponse{Error: "server returned 405", Status: 405}
	}
	if req.Kind == "send" {
		id := req.ID
		if id == "" {
			id = fmt.Sprintf("FAKE_%d", len(s.captures)+1)
		}
		s.captures = append(s.captures, Capture{ID: id, To: req.To, Message: req.Message, Session: se.key, Number: se.number})
		if s.transcripts[se.number] == nil {
			s.transcripts[se.number] = map[string][]actorMessage{}
		}
		kind, text, path := summarizeMessage(req.Message)
		s.transcripts[se.number][req.To] = append(s.transcripts[se.number][req.To], actorMessage{
			ID: id, FromMe: true, Kind: kind, Text: text, Timestamp: time.Now().UnixMilli(), Status: "sent", MediaURL: actorMediaURL(se.number, path),
		})
		if cfg.Echo || cfg.Scenario == "echo" {
			s.appendEvent(se, Event{Kind: "message", ID: id, Chat: req.To, Sender: se.number + "@s.whatsapp.net", FromMe: true, Message: req.Message})
		}
		if cfg.Scenario == "commit_drop_ack" {
			return OperationResponse{Error: "simulated lost acknowledgement after peer commit"}
		}
		return OperationResponse{ID: id}
	}
	if result, ok := s.performResource(se, req); ok {
		return result
	}
	return OperationResponse{Error: "unsupported fake WhatsApp operation: " + req.Kind}
}

func (s *Server) releaseSend(w http.ResponseWriter, key string) {
	s.mu.Lock()
	se := s.getSession(key)
	if se.release != nil {
		close(se.release)
		se.release = nil
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) injectNumber(w http.ResponseWriter, r *http.Request, number string) {
	if err := validateNumber(number); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req MessageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateMessage(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	count := 0
	for _, se := range s.sessions {
		if se.number != number || !se.connected {
			continue
		}
		s.appendEvent(se, messageEvent(req))
		count++
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]int{"delivered": count})
}

func (s *Server) injectSession(w http.ResponseWriter, r *http.Request, key string) {
	var req MessageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateMessage(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	se := s.getSession(key)
	s.appendEvent(se, messageEvent(req))
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func validateMessage(req MessageRequest) error {
	if req.Chat == "" || req.Sender == "" {
		return errors.New("chat and sender required")
	}
	if len(req.Message) == 0 {
		return errors.New("message required")
	}
	if err := protojson.Unmarshal(req.Message, &waE2E.Message{}); err != nil {
		return fmt.Errorf("invalid WhatsApp message: %w", err)
	}
	return nil
}

func messageEvent(req MessageRequest) Event {
	return Event{Kind: "message", ID: req.ID, Chat: req.Chat, Sender: req.Sender, SenderAlt: req.SenderAlt, PushName: req.PushName,
		FromMe: req.FromMe, Timestamp: timestampOrNow(req.Timestamp), Message: req.Message}
}

func (s *Server) injectEvents(w http.ResponseWriter, r *http.Request, key string) {
	var events []Event
	if !decodeJSON(w, r, &events) {
		return
	}
	for _, event := range events {
		if !validEventKind(event.Kind) {
			http.Error(w, "unsupported event kind: "+event.Kind, http.StatusBadRequest)
			return
		}
		if event.Kind == "message" {
			if err := validateMessage(MessageRequest{Chat: event.Chat, Sender: event.Sender, Message: event.Message}); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if len(event.Data) > 0 && !json.Valid(event.Data) {
			http.Error(w, "invalid event data", http.StatusBadRequest)
			return
		}
	}
	s.mu.Lock()
	se := s.getSession(key)
	for _, event := range events {
		s.appendEvent(se, event)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]int{"injected": len(events)})
}

func validEventKind(kind string) bool {
	switch kind {
	case "message", "chat_presence", "receipt", "paired", "connected", "disconnected", "qr", "logged_out", "stream_replaced", "temporary_ban", "outdated", "client_outdated":
		return true
	default:
		return false
	}
}

func (s *Server) firehose(w http.ResponseWriter, r *http.Request, number string) {
	if err := validateNumber(number); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req FirehoseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateMessage(req.MessageRequest); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Count < 0 || req.IntervalMS < 0 {
		http.Error(w, "count and interval_ms must be nonnegative", http.StatusBadRequest)
		return
	}
	for i := 0; i < req.Count; i++ {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		s.mu.Lock()
		for _, se := range s.sessions {
			if se.number != number || !se.connected {
				continue
			}
			event := messageEvent(req.MessageRequest)
			if event.ID == "" {
				s.inboundSequence++
				event.ID = fmt.Sprintf("FAKE_IN_%d", s.inboundSequence)
			}
			s.appendEvent(se, event)
		}
		s.mu.Unlock()
		if req.IntervalMS > 0 && i+1 < req.Count {
			timer := time.NewTimer(time.Duration(req.IntervalMS) * time.Millisecond)
			select {
			case <-timer.C:
			case <-r.Context().Done():
				timer.Stop()
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"emitted": req.Count})
}

func operationPayloadString(raw json.RawMessage) string {
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return strings.TrimSpace(string(raw))
}

func summarizeMessage(raw json.RawMessage) (kind, text, path string) {
	var message waE2E.Message
	if err := protojson.Unmarshal(raw, &message); err != nil {
		return "unknown", "", ""
	}
	switch {
	case message.GetConversation() != "":
		return "text", message.GetConversation(), ""
	case message.GetExtendedTextMessage() != nil:
		return "text", message.GetExtendedTextMessage().GetText(), ""
	case message.GetImageMessage() != nil:
		return "image", message.GetImageMessage().GetCaption(), message.GetImageMessage().GetDirectPath()
	case message.GetVideoMessage() != nil:
		return "video", message.GetVideoMessage().GetCaption(), message.GetVideoMessage().GetDirectPath()
	case message.GetAudioMessage() != nil:
		return "audio", "", message.GetAudioMessage().GetDirectPath()
	case message.GetDocumentMessage() != nil:
		return "document", message.GetDocumentMessage().GetCaption(), message.GetDocumentMessage().GetDirectPath()
	case message.GetStickerMessage() != nil:
		return "sticker", "", message.GetStickerMessage().GetDirectPath()
	case message.GetLocationMessage() != nil:
		return "location", message.GetLocationMessage().GetName(), ""
	case message.GetContactMessage() != nil:
		return "contact", message.GetContactMessage().GetDisplayName(), ""
	case message.GetPollCreationMessage() != nil:
		return "poll", message.GetPollCreationMessage().GetName(), ""
	default:
		return "other", "", ""
	}
}
