package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/coder/websocket"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

func (infra *e2eInfra) request(
	t *testing.T,
	method, path, key string,
	requestBody any,
	responseBody any,
	additionalHeaders map[string]string,
) int {
	t.Helper()
	var body io.Reader
	if requestBody != nil {
		raw, err := json.Marshal(requestBody)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	ctx, cancel := e2eContext(t)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, infra.apiURL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Api-Key", key)
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range additionalHeaders {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		raw, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			t.Fatalf("read %s %s error response: %v", method, path, readErr)
		}
		t.Logf("%s %s returned %d: %s", method, path, response.StatusCode, raw)
		response.Body = io.NopCloser(bytes.NewReader(raw))
	}
	if responseBody != nil {
		if err := json.NewDecoder(response.Body).Decode(responseBody); err != nil {
			t.Fatalf("decode %s %s response (%d): %v", method, path, response.StatusCode, err)
		}
	} else {
		_, _ = io.Copy(io.Discard, response.Body)
	}
	return response.StatusCode
}

func (infra *e2eInfra) send(
	t *testing.T,
	key, idempotencyKey string,
	request domain.SendRequest,
) (int, outbound.SendResult) {
	t.Helper()
	var result outbound.SendResult
	status := infra.request(t,
		http.MethodPost,
		"/api/v1/sessions/"+e2eSessionID+"/messages",
		key, request, &result,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	return status, result
}

func (infra *e2eInfra) messages(
	t *testing.T,
	key, chat string,
) (int, []domain.Message) {
	t.Helper()
	var page struct {
		Data []domain.Message `json:"data"`
	}
	status := infra.request(t,
		http.MethodGet,
		"/api/v1/sessions/"+e2eSessionID+"/chats/"+url.PathEscape(chat)+"/messages",
		key, nil, &page, nil,
	)
	return status, page.Data
}

func e2eJSON(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	value := map[string]any{}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func (infra *e2eInfra) realtimeSession(t *testing.T, key string) *websocket.Conn {
	t.Helper()
	var ticket struct {
		URL string `json:"url"`
	}
	status := infra.request(t, http.MethodPost, "/api/v1/realtime/ticket", key,
		map[string]any{"scope": "session", "session": e2eSessionID}, &ticket, nil)
	if status != http.StatusCreated || ticket.URL == "" {
		t.Fatalf("realtime ticket: status=%d url=%q", status, ticket.URL)
	}
	ctx, cancel := e2eContext(t)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, ticket.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	var connected map[string]any
	_, frame, err := conn.Read(ctx)
	if err != nil {
		_ = conn.CloseNow()
		t.Fatal(err)
	}
	if err := json.Unmarshal(frame, &connected); err != nil || connected["event"] != "connected" {
		_ = conn.CloseNow()
		t.Fatalf("realtime first frame = %s, err=%v", frame, err)
	}
	return conn
}

func e2eReadEvent(t *testing.T, conn *websocket.Conn, wantType string) domain.Event {
	t.Helper()
	ctx, cancel := e2eContext(t)
	defer cancel()
	for {
		_, frame, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event domain.Event
		if err := json.Unmarshal(frame, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == wantType {
			return event
		}
	}
}
