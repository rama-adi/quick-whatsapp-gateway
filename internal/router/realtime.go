package router

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/stream"
)

// Realtime endpoints (D5/D5a). The router is the single client-facing realtime
// endpoint: a browser cannot set an Authorization header on a WebSocket, so authz
// happens once at ticket mint (where the bearer + full principal are available) and
// the WS handshake merely redeems a short-lived, single-use Redis ticket.
const (
	ticketTTL      = 30 * time.Second
	realtimeWSPath = "/api/v1/realtime"
	wsWriteTimeout = 10 * time.Second
)

// ticket is the resolved, already-authorized subscription stored in Redis. The WS
// redeem reads exactly this — never the raw request — so the handshake trusts a
// scope that was authorized at mint.
type ticket struct {
	Scope        string          `json:"scope"` // session|organization|firehose
	Organization string          `json:"organization"`
	Session      string          `json:"session"`
	Events       []string        `json:"events"`
	Since        string          `json:"since"`
	Gateway      string          `json:"gateway"`
	Principal    ticketPrincipal `json:"principal"`
}

type ticketPrincipal struct {
	UserID         string `json:"userId"`
	KeyID          string `json:"keyId"`
	OrganizationID string `json:"organizationId"`
	OrgRole        string `json:"orgRole"`
	Role           string `json:"role"`
}

type ticketRequest struct {
	Scope   string   `json:"scope"`
	Session string   `json:"session"`
	Events  []string `json:"events"`
	Since   string   `json:"since"`
	Gateway string   `json:"gateway"`
}

type ticketResponse struct {
	Ticket           string `json:"ticket"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
	URL              string `json:"url"`
}

// realtimeEnabled reports whether the realtime endpoints are wired (Redis + pump
// present). When disabled the routes 503 rather than panic.
func (s *Server) realtimeEnabled() bool { return s.redis != nil && s.pump != nil }

// handleTicketMint authorizes a subscription request and stores the resolved scope
// in Redis under a single-use ticket id (D5a). Authorization is enforced HERE.
func (s *Server) handleTicketMint(w http.ResponseWriter, r *http.Request) {
	if !s.realtimeEnabled() {
		httpx.WriteError(w, domain.ErrUnavailable("realtime is not enabled"))
		return
	}
	p := authz.FromContext(r.Context())
	if p == nil {
		httpx.WriteError(w, domain.ErrUnauthorized("authentication required"))
		return
	}

	var req ticketRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		httpx.WriteError(w, domain.ErrValidation("invalid ticket request body"))
		return
	}
	if req.Scope == "" {
		// An empty body defaults to the caller's organization scope.
		req.Scope = "organization"
	}
	if len(req.Events) == 0 {
		req.Events = []string{"*"}
	}

	tk, err := s.authorizeTicket(r.Context(), p, req)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}

	id, err := newTicketID()
	if err != nil {
		httpx.WriteError(w, domain.ErrInternal("failed to mint ticket"))
		return
	}
	payload, _ := json.Marshal(tk)
	if err := s.redis.Set(r.Context(), s.ticketKey(id), payload, ticketTTL).Err(); err != nil {
		s.log.Error("store realtime ticket failed", "err", err)
		httpx.WriteError(w, domain.ErrInternal("failed to mint ticket"))
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, ticketResponse{
		Ticket:           id,
		ExpiresInSeconds: int(ticketTTL.Seconds()),
		URL:              s.wsURL(id),
	})
}

// authorizeTicket validates the requested scope against the principal and returns
// the resolved ticket. This is the load-bearing authz check (D5a).
func (s *Server) authorizeTicket(ctx context.Context, p *authz.Principal, req ticketRequest) (ticket, error) {
	tp := ticketPrincipal{
		UserID: p.UserID, KeyID: p.KeyID, OrganizationID: p.OrganizationID,
		OrgRole: p.OrgRole, Role: p.PlatformRole,
	}
	switch req.Scope {
	case "firehose":
		if !p.IsSuperAdmin() {
			return ticket{}, domain.ErrForbidden("firehose requires super_admin")
		}
		return ticket{Scope: "firehose", Events: req.Events, Gateway: req.Gateway, Principal: tp}, nil

	case "organization":
		if !authz.Allow(p, authz.CapEvents) {
			return ticket{}, domain.ErrForbidden("missing required capability: events")
		}
		if p.OrganizationID == "" {
			return ticket{}, domain.ErrForbidden("no active organization")
		}
		return ticket{Scope: "organization", Organization: p.OrganizationID, Events: req.Events, Since: req.Since, Principal: tp}, nil

	case "session":
		if req.Session == "" {
			return ticket{}, domain.ErrValidation("session is required for scope=session")
		}
		if !authz.Allow(p, authz.CapEvents) {
			return ticket{}, domain.ErrForbidden("missing required capability: events")
		}
		sess, err := s.sessions.Get(ctx, req.Session)
		if err != nil {
			return ticket{}, domain.ErrNotFound("session not found")
		}
		if !p.IsSuperAdmin() && sess.OrganizationID != p.OrganizationID {
			return ticket{}, domain.ErrNotFound("session not found")
		}
		return ticket{Scope: "session", Organization: sess.OrganizationID, Session: req.Session, Events: req.Events, Since: req.Since, Principal: tp}, nil

	default:
		return ticket{}, domain.ErrValidation("scope must be one of session|organization|firehose")
	}
}

// handleRealtimeWS redeems a ticket (atomic GETDEL → single-use) and serves the
// subscription over a WebSocket. No re-authn: it trusts the redeemed scope.
func (s *Server) handleRealtimeWS(w http.ResponseWriter, r *http.Request) {
	if !s.realtimeEnabled() {
		httpx.WriteError(w, domain.ErrUnavailable("realtime is not enabled"))
		return
	}
	id := r.URL.Query().Get("ticket")
	if id == "" {
		httpx.WriteError(w, domain.ErrUnauthorized("missing ticket"))
		return
	}
	raw, err := s.redis.GetDel(r.Context(), s.ticketKey(id)).Result()
	if err != nil || raw == "" {
		// Either never existed, expired, or already redeemed (single-use).
		httpx.WriteError(w, domain.ErrUnauthorized("invalid or expired ticket"))
		return
	}
	var tk ticket
	if err := json.Unmarshal([]byte(raw), &tk); err != nil {
		httpx.WriteError(w, domain.ErrInternal("corrupt ticket"))
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.wsOrigins})
	if err != nil {
		s.log.Debug("websocket accept failed", "err", err)
		return // Accept already wrote the error response
	}
	defer conn.CloseNow()

	// CloseRead returns a context cancelled when the peer closes or errors, and it
	// drains incoming frames so control frames (ping/close) are handled.
	ctx, cancel := context.WithCancel(conn.CloseRead(r.Context()))
	defer cancel()

	// Register so the control bus can drop this connection on revocation (D5).
	if s.registry != nil {
		id := stream.ConnIdentity{
			KeyID:          tk.Principal.KeyID,
			UserID:         tk.Principal.UserID,
			OrganizationID: tk.Principal.OrganizationID,
		}
		deregister := s.registry.Register(id, cancel)
		defer deregister()
	}

	scope := stream.Scope{Organization: tk.Organization, Session: tk.Session, GatewaySource: tk.Gateway}
	sink := &wsSink{conn: conn}
	if err := s.pump.Run(ctx, sink, scope, tk.Events, tk.Since); err != nil {
		s.log.Debug("realtime pump ended", "err", err)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// wsSink writes pump payloads as WebSocket text frames.
type wsSink struct{ conn *websocket.Conn }

func (s *wsSink) Send(ctx context.Context, payload []byte) error {
	wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return s.conn.Write(wctx, websocket.MessageText, payload)
}

func (s *Server) ticketKey(id string) string { return s.redisPrefix + ":rt:ticket:" + id }

func (s *Server) wsURL(id string) string {
	base := s.publicURL
	if base == "" {
		return realtimeWSPath + "?ticket=" + id
	}
	scheme := "wss"
	if len(base) >= 5 && base[:5] == "http:" {
		scheme = "ws"
	}
	// strip scheme://host from publicURL, rebuild with ws(s)
	host := base
	if i := indexAfterScheme(base); i >= 0 {
		host = base[i:]
	}
	return scheme + "://" + host + realtimeWSPath + "?ticket=" + id
}

func indexAfterScheme(u string) int {
	const sep = "://"
	for i := 0; i+len(sep) <= len(u); i++ {
		if u[i:i+len(sep)] == sep {
			return i + len(sep)
		}
	}
	return -1
}

func newTicketID() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "rt_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// realtimeRedis is the minimal Redis surface realtime needs: SET with TTL to mint
// a ticket and atomic GETDEL to redeem it exactly once (Redis 6.2+). *redis.Client
// satisfies it; tests fake it.
type realtimeRedis interface {
	Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd
	GetDel(ctx context.Context, key string) *redis.StringCmd
}
