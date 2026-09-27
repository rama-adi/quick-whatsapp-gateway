package fakewhatsapp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/fakewhatsapp/scenario"
)

// Server is the stateful simulated WhatsApp peer. The gateway reaches it over
// HTTP, so the same actor can run as a standalone dev service or in an E2E run.
type Server struct {
	mu              sync.Mutex
	numbers         map[string]Config
	sessions        map[string]*session
	captures        []Capture
	operations      []Operation
	groups          map[string]*types.GroupInfo
	groupOwners     map[string]string
	groupAccess     map[string]map[string]bool
	invites         map[string]string
	inviteSequence  uint64
	groupSequence   uint64
	inboundSequence uint64
	attemptTrace    []Attempt
	eventTrace      []EventTrace
	media           map[string][]byte
	transcripts     map[string]map[string][]actorMessage
	blocklists      map[string]map[string]bool
	contactNames    map[string]map[string]string
	scenarios       map[string]scenario.Story
	attempts        int
	blocked         int
	stateRevision   uint64
	stateNotify     chan struct{}
}

type session struct {
	key       string
	number    string
	connected bool
	paired    bool
	pending   bool
	attempts  int
	blocked   int
	events    []Event
	notify    chan struct{}
	release   chan struct{}
}

func NewServer() *Server {
	server := &Server{
		numbers:      map[string]Config{},
		sessions:     map[string]*session{},
		captures:     []Capture{},
		operations:   []Operation{},
		groups:       map[string]*types.GroupInfo{},
		groupOwners:  map[string]string{},
		groupAccess:  map[string]map[string]bool{},
		invites:      map[string]string{},
		attemptTrace: []Attempt{},
		eventTrace:   []EventTrace{},
		media:        map[string][]byte{},
		transcripts:  map[string]map[string][]actorMessage{},
		blocklists:   map[string]map[string]bool{},
		contactNames: map[string]map[string]string{},
		stateNotify:  make(chan struct{}),
	}
	server.scenarios = registeredScenarios()
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if s.scenarioRoute(w, r, parts) || s.actorRoute(w, r, parts) {
		return
	}
	switch {
	case r.Method == http.MethodGet && (path == "" || path == "panel"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(panelHTML)
	case r.Method == http.MethodGet && path == "v1/state/events":
		s.stateEvents(w, r)
	case r.Method == http.MethodGet && path == "v1/state":
		writeJSON(w, http.StatusOK, s.snapshot())
	case len(parts) == 3 && parts[0] == "v1" && parts[1] == "numbers" && r.Method == http.MethodPost:
		s.configure(w, r, parts[2])
	case len(parts) == 4 && parts[0] == "v1" && parts[1] == "numbers" && parts[3] == "messages" && r.Method == http.MethodPost:
		s.injectNumber(w, r, parts[2])
	case len(parts) == 4 && parts[0] == "v1" && parts[1] == "numbers" && parts[3] == "firehose" && r.Method == http.MethodPost:
		s.firehose(w, r, parts[2])
	case len(parts) == 4 && parts[0] == "v1" && parts[1] == "numbers" && parts[3] == "confirm" && r.Method == http.MethodPost:
		s.confirmNumber(w, r, parts[2])
	case len(parts) == 4 && parts[0] == "v1" && parts[1] == "sessions":
		s.sessionRoute(w, r, parts[2], parts[3])
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) sessionRoute(w http.ResponseWriter, r *http.Request, key, action string) {
	if key == "" {
		http.Error(w, "session key required", http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == http.MethodPost && action == "connect":
		s.connect(w, r, key)
	case r.Method == http.MethodPost && action == "disconnect":
		s.disconnect(w, key)
	case r.Method == http.MethodPost && action == "pair":
		s.pair(w, r, key)
	case r.Method == http.MethodPost && action == "confirm":
		s.confirm(w, r, key)
	case r.Method == http.MethodPost && action == "operations":
		s.operation(w, r, key)
	case r.Method == http.MethodPost && action == "release":
		s.releaseSend(w, key)
	case r.Method == http.MethodPost && action == "messages":
		s.injectSession(w, r, key)
	case r.Method == http.MethodPost && action == "events":
		s.injectEvents(w, r, key)
	case r.Method == http.MethodGet && action == "events":
		s.pollEvents(w, r, key)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) getSession(key string) *session {
	se := s.sessions[key]
	if se == nil {
		se = &session{key: key, notify: make(chan struct{}), events: []Event{}}
		s.sessions[key] = se
	}
	return se
}

func (s *Server) config(number string) Config {
	cfg, ok := s.numbers[number]
	if !ok || cfg.Scenario == "" {
		cfg.Scenario = "healthy"
	}
	return cfg
}

func validateNumber(number string) error {
	if !strings.HasPrefix(number, "555") {
		return errors.New("fake phone number must begin with 555")
	}
	for _, ch := range number {
		if ch < '0' || ch > '9' {
			return errors.New("fake phone number must contain digits only")
		}
	}
	return nil
}

func (s *Server) configure(w http.ResponseWriter, r *http.Request, number string) {
	if err := validateNumber(number); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var cfg Config
	if !decodeJSON(w, r, &cfg) {
		return
	}
	switch cfg.Scenario {
	case "", "healthy", "connect_error", "disconnect", "send_error", "server_405", "upload_error", "block_send", "commit_drop_ack", "echo":
	default:
		http.Error(w, "unknown scenario", http.StatusBadRequest)
		return
	}
	if cfg.Scenario == "" {
		cfg.Scenario = "healthy"
	}
	s.mu.Lock()
	s.numbers[number] = cfg
	s.bumpState()
	if cfg.Scenario == "disconnect" || cfg.Scenario == "connect_error" {
		for _, se := range s.sessions {
			if se.number == number && se.connected {
				s.appendEvent(se, Event{Kind: "disconnected", Error: "simulated WhatsApp disconnect"})
			}
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request, key string) {
	var req ConnectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Number != "" {
		if err := validateNumber(req.Number); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	s.mu.Lock()
	se := s.getSession(key)
	if req.Number != "" {
		se.number, se.paired = req.Number, true
	}
	cfg := s.config(se.number)
	if cfg.Scenario == "connect_error" {
		s.mu.Unlock()
		message := cfg.ConnectError
		if message == "" {
			message = "simulated WhatsApp connection failure"
		}
		writeJSON(w, http.StatusOK, ConnectResponse{Error: message})
		return
	}
	se.connected = true
	if se.paired {
		s.appendEvent(se, Event{Kind: "connected", Number: se.number})
	}
	if cfg.Scenario == "disconnect" {
		se.connected = false
		s.appendEvent(se, Event{Kind: "disconnected", Error: "simulated WhatsApp disconnect"})
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, ConnectResponse{Connected: true})
}

func (s *Server) disconnect(w http.ResponseWriter, key string) {
	s.mu.Lock()
	se := s.getSession(key)
	se.connected = false
	s.bumpState()
	if se.release != nil {
		close(se.release)
		se.release = nil
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request, key string) {
	var req PairRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Number != "" {
		if err := validateNumber(req.Number); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Method != "qr" && req.Method != "code" {
		http.Error(w, "method must be qr or code", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	se := s.getSession(key)
	se.pending = true
	if req.Number != "" {
		se.number = req.Number
	}
	code := "FAKE-" + key
	if req.Method == "qr" {
		s.appendEvent(se, Event{Kind: "qr", Code: code})
	}
	cfg := s.config(se.number)
	if cfg.AutoPair && se.number != "" {
		s.pairSession(se, se.number)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, PairResponse{Code: code})
}

func (s *Server) pairSession(se *session, number string) {
	se.number, se.paired, se.pending, se.connected = number, true, false, true
	s.appendEvent(se, Event{Kind: "paired", Number: number})
	s.appendEvent(se, Event{Kind: "connected", Number: number})
}

func (s *Server) confirm(w http.ResponseWriter, r *http.Request, key string) {
	var req ConfirmRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateNumber(req.Number); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	se := s.getSession(key)
	if !se.pending {
		s.mu.Unlock()
		http.Error(w, "session is not awaiting pairing", http.StatusConflict)
		return
	}
	s.pairSession(se, req.Number)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) confirmNumber(w http.ResponseWriter, r *http.Request, number string) {
	if err := validateNumber(number); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		Session string `json:"session"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Session == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	se := s.getSession(req.Session)
	if !se.pending {
		s.mu.Unlock()
		http.Error(w, "session is not awaiting pairing", http.StatusConflict)
		return
	}
	s.pairSession(se, number)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) appendEvent(se *session, event Event) {
	event.Sequence = uint64(len(se.events) + 1)
	se.events = append(se.events, event)
	s.bumpState()
	s.eventTrace = append(s.eventTrace, EventTrace{Session: se.key, Event: event})
	switch event.Kind {
	case "connected", "paired":
		se.connected = true
	case "disconnected", "logged_out", "stream_replaced", "temporary_ban", "outdated", "client_outdated":
		se.connected = false
	}
	close(se.notify)
	se.notify = make(chan struct{})
}

func (s *Server) pollEvents(w http.ResponseWriter, r *http.Request, key string) {
	after, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	if err != nil {
		http.Error(w, "invalid after sequence", http.StatusBadRequest)
		return
	}
	for {
		s.mu.Lock()
		se := s.getSession(key)
		if after < uint64(len(se.events)) {
			result := append([]Event{}, se.events[after:]...)
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, EventsResponse{Events: result})
			return
		}
		wait := se.notify
		s.mu.Unlock()
		select {
		case <-wait:
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := State{
		Numbers: map[string]Config{}, Sessions: []SessionState{}, Captures: append([]Capture{}, s.captures...),
		Operations: append([]Operation{}, s.operations...), Attempts: s.attempts, Blocked: s.blocked,
		AttemptTrace: append([]Attempt{}, s.attemptTrace...), EventTrace: append([]EventTrace{}, s.eventTrace...),
	}
	for number, cfg := range s.numbers {
		result.Numbers[number] = cfg
	}
	for _, se := range s.sessions {
		result.Sessions = append(result.Sessions, SessionState{
			Key: se.key, Number: se.number, Connected: se.connected, Paired: se.paired,
			Pending: se.pending, Attempts: se.attempts, Blocked: se.blocked,
		})
	}
	sort.Slice(result.Sessions, func(i, j int) bool { return result.Sessions[i].Key < result.Sessions[j].Key })
	return result
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func hashUpload(data []byte) *UploadResult {
	sum := sha256.Sum256(data)
	hash := base64.StdEncoding.EncodeToString(sum[:])
	return &UploadResult{URL: "https://fake.whatsapp.invalid/media", DirectPath: "/media/" + fmt.Sprintf("%x", sum[:8]), FileLength: uint64(len(data)), FileSHA256: hash, FileEncSHA256: hash, MediaKey: hash}
}

func timestampOrNow(timestamp int64) int64 {
	if timestamp != 0 {
		return timestamp
	}
	return time.Now().UnixMilli()
}

// bumpState is called while s.mu is held. It wakes every state watcher without
// an arbitrary refresh timer.
func (s *Server) bumpState() {
	s.stateRevision++
	close(s.stateNotify)
	s.stateNotify = make(chan struct{})
}

func (s *Server) stateEvents(w http.ResponseWriter, r *http.Request) {
	after, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	if err != nil {
		http.Error(w, "invalid after revision", http.StatusBadRequest)
		return
	}
	for {
		s.mu.Lock()
		if after < s.stateRevision {
			revision := s.stateRevision
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]uint64{"revision": revision})
			return
		}
		wait := s.stateNotify
		s.mu.Unlock()
		select {
		case <-wait:
		case <-r.Context().Done():
			return
		}
	}
}
