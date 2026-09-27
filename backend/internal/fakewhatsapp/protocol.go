package fakewhatsapp

import "encoding/json"

// Config selects behavior for one plain fake phone identity. A scenario can be
// changed while a gateway is connected; it is never encoded in the phone number.
type Config struct {
	Scenario     string `json:"scenario"`
	AutoPair     bool   `json:"auto_pair"`
	Echo         bool   `json:"echo"`
	ConnectError string `json:"connect_error,omitempty"`
	SendError    string `json:"send_error,omitempty"`
	UploadError  string `json:"upload_error,omitempty"`
}

type ConnectRequest struct {
	Number string `json:"number"`
}
type ConnectResponse struct {
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}
type PairRequest struct {
	Number string `json:"number"`
	Method string `json:"method"`
}
type PairResponse struct {
	Code  string `json:"code"`
	Error string `json:"error,omitempty"`
}
type ConfirmRequest struct {
	Number string `json:"number"`
}

type Event struct {
	Sequence  uint64          `json:"sequence"`
	Kind      string          `json:"kind"`
	Number    string          `json:"number,omitempty"`
	Code      string          `json:"code,omitempty"`
	Error     string          `json:"error,omitempty"`
	ID        string          `json:"id,omitempty"`
	Chat      string          `json:"chat,omitempty"`
	Sender    string          `json:"sender,omitempty"`
	PushName  string          `json:"push_name,omitempty"`
	SenderAlt string          `json:"sender_alt,omitempty"`
	FromMe    bool            `json:"from_me,omitempty"`
	Timestamp int64           `json:"timestamp,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}
type EventsResponse struct {
	Events []Event `json:"events"`
}

type OperationRequest struct {
	Kind       string          `json:"kind"`
	To         string          `json:"to,omitempty"`
	ID         string          `json:"id,omitempty"`
	Message    json.RawMessage `json:"message,omitempty"`
	DataBase64 string          `json:"data_base64,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}
type UploadResult struct {
	URL           string `json:"url"`
	DirectPath    string `json:"direct_path"`
	FileLength    uint64 `json:"file_length"`
	FileSHA256    string `json:"file_sha256"`
	FileEncSHA256 string `json:"file_enc_sha256"`
	MediaKey      string `json:"media_key"`
}
type OperationResponse struct {
	ID     string          `json:"id,omitempty"`
	Error  string          `json:"error,omitempty"`
	Status int             `json:"status,omitempty"`
	Upload *UploadResult   `json:"upload,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}
type Capture struct {
	ID      string          `json:"id"`
	To      string          `json:"to"`
	Message json.RawMessage `json:"message"`
	Session string          `json:"session"`
	Number  string          `json:"number"`
}
type Operation struct {
	Session string          `json:"session"`
	Number  string          `json:"number"`
	Kind    string          `json:"kind"`
	To      string          `json:"to,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}
type SessionState struct {
	Key       string `json:"key"`
	Number    string `json:"number,omitempty"`
	Connected bool   `json:"connected"`
	Paired    bool   `json:"paired"`
	Pending   bool   `json:"pending"`
	Attempts  int    `json:"attempts"`
	Blocked   int    `json:"blocked"`
}
type Attempt struct {
	Session string `json:"session"`
	Number  string `json:"number"`
	ID      string `json:"id,omitempty"`
	To      string `json:"to"`
	Outcome string `json:"outcome"`
}
type EventTrace struct {
	Session string `json:"session"`
	Event   Event  `json:"event"`
}
type State struct {
	Numbers      map[string]Config `json:"numbers"`
	Sessions     []SessionState    `json:"sessions"`
	Captures     []Capture         `json:"captures"`
	Operations   []Operation       `json:"operations"`
	Attempts     int               `json:"attempts"`
	Blocked      int               `json:"blocked"`
	AttemptTrace []Attempt         `json:"attempt_trace"`
	EventTrace   []EventTrace      `json:"event_trace"`
}
type MessageRequest struct {
	ID        string          `json:"id,omitempty"`
	Chat      string          `json:"chat"`
	Sender    string          `json:"sender"`
	PushName  string          `json:"push_name,omitempty"`
	SenderAlt string          `json:"sender_alt,omitempty"`
	FromMe    bool            `json:"from_me,omitempty"`
	Timestamp int64           `json:"timestamp,omitempty"`
	Message   json.RawMessage `json:"message"`
}
type FirehoseRequest struct {
	MessageRequest
	Count      int   `json:"count"`
	IntervalMS int64 `json:"interval_ms"`
}
