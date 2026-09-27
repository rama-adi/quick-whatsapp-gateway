package fakewhatsapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"

	"go.mau.fi/whatsmeow/types"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/fakewhatsapp/scenario"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/fakewhatsapp/scenarios"
)

type ScenarioInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type StepResult struct {
	Index     int    `json:"index"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Delivered int    `json:"delivered"`
	ID        string `json:"id,omitempty"`
	JID       string `json:"jid,omitempty"`
	Error     string `json:"error,omitempty"`
}
type ScenarioResult struct {
	ScenarioID string       `json:"scenario_id"`
	Number     string       `json:"number"`
	Steps      []StepResult `json:"steps"`
	Delivered  int          `json:"delivered"`
	TotalSteps int          `json:"total_steps"`
	FailedAt   *int         `json:"failed_at,omitempty"`
}

type scenarioRun struct {
	aliases       map[string]string
	lastMessageID string
}

func registeredScenarios() map[string]scenario.Story {
	result := map[string]scenario.Story{}
	for _, story := range scenarios.Catalog() {
		if err := validateStory(story); err != nil {
			panic(fmt.Sprintf("fake WhatsApp scenario %q: %v", story.ID, err))
		}
		if _, exists := result[story.ID]; exists {
			panic(fmt.Sprintf("duplicate fake WhatsApp scenario %q", story.ID))
		}
		result[story.ID] = story
	}
	return result
}

func (s *Server) scenarioRoute(w http.ResponseWriter, r *http.Request, parts []string) bool {
	if r.Method == http.MethodGet && r.URL.Path == "/v1/scenarios" {
		infos := make([]ScenarioInfo, 0, len(s.scenarios))
		for _, story := range s.scenarios {
			infos = append(infos, ScenarioInfo{ID: story.ID, Name: story.Name, Description: story.Description})
		}
		sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
		writeJSON(w, http.StatusOK, map[string]any{"scenarios": infos})
		return true
	}
	if r.Method != http.MethodPost || len(parts) != 5 || parts[0] != "v1" || parts[1] != "numbers" || parts[3] != "scenarios" {
		return false
	}
	number, id := parts[2], parts[4]
	if err := validateNumber(number); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}
	story, ok := s.scenarios[id]
	if !ok {
		http.Error(w, "scenario not found", http.StatusNotFound)
		return true
	}
	result := s.runScenario(r.Context(), number, story)
	status := http.StatusOK
	if result.FailedAt != nil {
		status = http.StatusConflict
	}
	writeJSON(w, status, result)
	return true
}

func (s *Server) runScenario(ctx context.Context, number string, story scenario.Story) ScenarioResult {
	result := ScenarioResult{ScenarioID: story.ID, Number: number, Steps: []StepResult{}, TotalSteps: len(story.Steps)}
	run := &scenarioRun{aliases: map[string]string{}}
	for index, step := range story.Steps {
		select {
		case <-ctx.Done():
			result.FailedAt = &index
			result.Steps = append(result.Steps, StepResult{Index: index, Type: stepKind(step), Status: "failed", Error: ctx.Err().Error()})
			return result
		default:
		}
		item := s.runStep(ctx, number, step, run)
		item.Index = index
		item.Type = stepKind(step)
		result.Steps = append(result.Steps, item)
		result.Delivered += item.Delivered
		if item.Status != "ok" {
			result.FailedAt = &index
			return result
		}
	}
	return result
}

func stepKind(step scenario.Step) string {
	switch step.(type) {
	case scenario.Send:
		return "send"
	case scenario.GroupCreate:
		return "create_group"
	case scenario.Behavior:
		return "config"
	case scenario.Effect:
		return "effect"
	default:
		return "unknown"
	}
}
func (s *Server) runStep(ctx context.Context, number string, step scenario.Step, run *scenarioRun) StepResult {
	switch typed := step.(type) {
	case scenario.Send:
		return s.runSendStep(ctx, number, typed, run)
	case scenario.GroupCreate:
		return s.runGroupStep(ctx, number, typed, run)
	case scenario.Behavior:
		return s.runBehaviorStep(ctx, number, typed)
	case scenario.Effect:
		return s.runEffectStep(ctx, number, typed, run)
	default:
		return failedStep(fmt.Errorf("unsupported scenario step %T", step))
	}
}
func (s *Server) runSendStep(ctx context.Context, number string, step scenario.Send, run *scenarioRun) StepResult {
	chat, err := resolveAlias(step.Chat, run)
	if err != nil {
		return failedStep(err)
	}
	quoteID := step.QuoteID
	if quoteID == scenario.LastMessage {
		quoteID = run.lastMessageID
	}
	if step.QuoteID != "" && quoteID == "" {
		return failedStep(fmt.Errorf("reply has no quoted message"))
	}
	body := actorSend{Chat: chat, QuoteID: quoteID, From: step.Sender, FromName: step.SenderName, Kind: string(step.Kind), Text: step.Text, Filename: step.Filename, MimeType: step.MimeType, DataBase64: encodeBytes(step.Data), Latitude: step.Latitude, Longitude: step.Longitude, ContactName: step.ContactName, ContactPhone: step.ContactPhone, PollOptions: step.PollOptions, Buttons: step.Buttons, ListRows: step.ListRows}
	var response struct {
		ID        string `json:"id"`
		Delivered int    `json:"delivered"`
	}
	if err := s.scenarioCall(ctx, "/v1/numbers/"+number+"/send", body, &response); err != nil {
		return failedStep(err)
	}
	run.lastMessageID = response.ID
	return StepResult{Status: "ok", ID: response.ID, Delivered: response.Delivered}
}
func (s *Server) runGroupStep(ctx context.Context, number string, step scenario.GroupCreate, run *scenarioRun) StepResult {
	var response struct {
		JID       string `json:"jid"`
		Delivered int    `json:"delivered"`
	}
	body := struct {
		Name         string   `json:"name"`
		Participants []string `json:"participants"`
	}{Name: step.Name, Participants: step.Participants}
	if err := s.scenarioCall(ctx, "/v1/numbers/"+number+"/groups", body, &response); err != nil {
		return failedStep(err)
	}
	run.aliases[step.Alias] = response.JID
	return StepResult{Status: "ok", JID: response.JID, Delivered: response.Delivered}
}
func (s *Server) runBehaviorStep(ctx context.Context, number string, step scenario.Behavior) StepResult {
	config := Config{Scenario: string(step.Mode), AutoPair: step.AutoPair, Echo: step.Echo}
	if err := s.scenarioCall(ctx, "/v1/numbers/"+number, config, nil); err != nil {
		return failedStep(err)
	}
	return StepResult{Status: "ok"}
}
func (s *Server) runEffectStep(ctx context.Context, number string, step scenario.Effect, run *scenarioRun) StepResult {
	chat, err := resolveAlias(step.Chat, run)
	if err != nil {
		return failedStep(err)
	}
	messageID := step.MessageID
	if messageID == "@last" {
		messageID = run.lastMessageID
	}
	if messageID == "" {
		return failedStep(fmt.Errorf("effect has no message id"))
	}
	body := struct {
		Chat      string `json:"chat"`
		MessageID string `json:"message_id"`
		Kind      string `json:"kind"`
		Value     string `json:"value"`
	}{Chat: chat, MessageID: messageID, Kind: step.Kind, Value: step.Value}
	var response struct {
		Delivered int `json:"delivered"`
	}
	if err := s.scenarioCall(ctx, "/v1/numbers/"+number+"/effects", body, &response); err != nil {
		return failedStep(err)
	}
	return StepResult{Status: "ok", ID: messageID, Delivered: response.Delivered}
}
func failedStep(err error) StepResult { return StepResult{Status: "failed", Error: err.Error()} }
func resolveAlias(value string, run *scenarioRun) (string, error) {
	if !strings.HasPrefix(value, "@") {
		return value, nil
	}
	resolved := run.aliases[strings.TrimPrefix(value, "@")]
	if resolved == "" {
		return "", fmt.Errorf("unknown group alias %q", value)
	}
	return resolved, nil
}
func encodeBytes(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(data)
}
func (s *Server) scenarioCall(ctx context.Context, path string, body any, result any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, req)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return fmt.Errorf("%s: %s", response.Status, strings.TrimSpace(recorder.Body.String()))
	}
	if result != nil {
		if err := json.NewDecoder(response.Body).Decode(result); err != nil {
			return err
		}
	}
	return nil
}

func validateStory(story scenario.Story) error {
	if story.ID == "" || strings.Contains(story.ID, "/") {
		return fmt.Errorf("id must be a nonempty URL segment")
	}
	if story.Name == "" {
		return fmt.Errorf("name required")
	}
	if len(story.Steps) == 0 {
		return fmt.Errorf("steps required")
	}
	aliases := map[string]bool{}
	seenSend := false
	for index, step := range story.Steps {
		if step == nil {
			return fmt.Errorf("step %d is nil", index)
		}
		switch typed := step.(type) {
		case scenario.Send:
			if typed.Chat == "" {
				return fmt.Errorf("send step %d has no chat", index)
			}
			if strings.HasPrefix(typed.Chat, "@") && !aliases[strings.TrimPrefix(typed.Chat, "@")] {
				return fmt.Errorf("send step %d references unknown group alias", index)
			}
			if !validMessageKind(typed.Kind) {
				return fmt.Errorf("send step %d has unsupported message kind %q", index, typed.Kind)
			}
			if !strings.HasPrefix(typed.Chat, "@") {
				if _, err := types.ParseJID(typed.Chat); err != nil {
					return fmt.Errorf("send step %d has invalid chat: %w", index, err)
				}
			}
			if typed.Sender != "" {
				if _, err := types.ParseJID(typed.Sender); err != nil {
					return fmt.Errorf("send step %d has invalid sender: %w", index, err)
				}
			}
			if typed.QuoteID == scenario.LastMessage && !seenSend {
				return fmt.Errorf("send step %d replies before a message", index)
			}
			if err := validateTypedSend(typed); err != nil {
				return fmt.Errorf("send step %d: %w", index, err)
			}
			seenSend = true
		case scenario.GroupCreate:
			if typed.Alias == "" || strings.Contains(typed.Alias, "@") || aliases[typed.Alias] {
				return fmt.Errorf("group step %d has invalid or duplicate alias", index)
			}
			if typed.Name == "" {
				return fmt.Errorf("group step %d has no name", index)
			}
			for _, number := range typed.Participants {
				if err := validateNumber(number); err != nil {
					return fmt.Errorf("group step %d participant: %w", index, err)
				}
			}
			aliases[typed.Alias] = true
		case scenario.Behavior:
			if !validScenarioMode(typed.Mode) {
				return fmt.Errorf("config step %d has unsupported mode %q", index, typed.Mode)
			}
		case scenario.Effect:
			if strings.HasPrefix(typed.Chat, "@") && !aliases[strings.TrimPrefix(typed.Chat, "@")] {
				return fmt.Errorf("effect step %d references unknown group alias", index)
			}
			if typed.MessageID == "@last" && !seenSend {
				return fmt.Errorf("effect step %d uses @last before a send", index)
			}
			if typed.Chat == "" || typed.MessageID == "" {
				return fmt.Errorf("effect step %d needs chat and message id", index)
			}
			switch typed.Kind {
			case "delivered", "read", "played", "reaction", "edit", "delete":
			default:
				return fmt.Errorf("effect step %d has unsupported effect %q", index, typed.Kind)
			}
		default:
			return fmt.Errorf("step %d has unsupported type %T", index, step)
		}
	}
	return nil
}
func validMessageKind(kind scenario.MessageKind) bool {
	switch kind {
	case scenario.TextMessage, scenario.ImageMessage, scenario.VideoMessage, scenario.AudioMessage, scenario.DocumentMessage, scenario.StickerMessage, scenario.LocationMessage, scenario.ContactMessage, scenario.PollMessage, scenario.ButtonsMessage, scenario.ListMessage:
		return true
	default:
		return false
	}
}
func validScenarioMode(mode scenario.Mode) bool {
	switch mode {
	case scenario.ModeHealthy, scenario.ModeConnectError, scenario.ModeDisconnect, scenario.ModeSendError, scenario.ModeServer405, scenario.ModeUploadError, scenario.ModeBlockSend, scenario.ModeCommitDropAck, scenario.ModeEcho:
		return true
	default:
		return false
	}
}
func validateTypedSend(step scenario.Send) error {
	switch step.Kind {
	case scenario.TextMessage:
		if step.Text == "" {
			return fmt.Errorf("text required")
		}
	case scenario.ImageMessage, scenario.VideoMessage, scenario.AudioMessage, scenario.DocumentMessage, scenario.StickerMessage:
		if len(step.Data) == 0 {
			return fmt.Errorf("media bytes required")
		}
		if step.MimeType == "" {
			return fmt.Errorf("media MIME type required")
		}
	case scenario.ContactMessage:
		if step.ContactName == "" || step.ContactPhone == "" {
			return fmt.Errorf("contact name and phone required")
		}
	case scenario.PollMessage:
		if step.Text == "" || len(step.PollOptions) == 0 {
			return fmt.Errorf("poll question and options required")
		}
	case scenario.ButtonsMessage:
		if step.Text == "" || len(step.Buttons) == 0 {
			return fmt.Errorf("buttons body and choices required")
		}
	case scenario.ListMessage:
		if step.Text == "" || len(step.ListRows) == 0 {
			return fmt.Errorf("list body and rows required")
		}
	}
	return nil
}
