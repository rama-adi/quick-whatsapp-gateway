package router

import (
	"context"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// SessionResolver is the slice of the session repo the router needs: look up a
// session to find its owning gateway and org. *store.SessionRepo satisfies it.
type SessionResolver interface {
	Get(ctx context.Context, id string) (domain.WASession, error)
}

// GatewayResolver is the slice of the gateway registry the router reads to route
// and place. *store.GatewayRepo satisfies it.
type GatewayResolver interface {
	Get(ctx context.Context, id string) (domain.Gateway, error)
	PickForPlacement(ctx context.Context) (domain.Gateway, error)
	ListActive(ctx context.Context) ([]domain.Gateway, error)
}

// resolveSessionGateway maps a session id to the gateway that owns it, enforcing
// org isolation (the load-bearing check now that the gateway no longer re-auths
// the end user) and gateway reachability.
//
//   - unknown session                         → 404 not_found
//   - session owned by another org (non-admin) → 404 (existence is not leaked)
//   - owning gateway missing/not usable        → 503 gateway_unavailable
func (s *Server) resolveSessionGateway(ctx context.Context, p *authz.Principal, sessionID string) (domain.Gateway, error) {
	sess, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return domain.Gateway{}, domain.ErrNotFound("session not found")
	}
	if !p.IsSuperAdmin() && sess.OrganizationID != p.OrganizationID {
		// Don't reveal that a session exists in another org.
		return domain.Gateway{}, domain.ErrNotFound("session not found")
	}
	g, err := s.gateways.Get(ctx, sess.GatewayID)
	if err != nil {
		return domain.Gateway{}, domain.ErrUnavailable("owning gateway is not registered")
	}
	if !s.gatewayUsable(g) {
		return domain.Gateway{}, domain.ErrUnavailable("owning gateway is unavailable")
	}
	return g, nil
}

// pickPlacementGateway chooses where to create a new session: the least-loaded
// active gateway with headroom. No candidate → 503.
func (s *Server) pickPlacementGateway(ctx context.Context) (domain.Gateway, error) {
	g, err := s.gateways.PickForPlacement(ctx)
	if err != nil {
		return domain.Gateway{}, domain.ErrUnavailable("no gateway available to place a new session")
	}
	if !s.gatewayUsable(g) {
		return domain.Gateway{}, domain.ErrUnavailable("no gateway available to place a new session")
	}
	return g, nil
}

// pickAnyActiveGateway chooses a target for gateway-agnostic routes (org-scoped
// reads/writes against shared MySQL: webhooks, the session list, admin oversight).
// Any active gateway serves these identically; we pick the least-loaded one.
func (s *Server) pickAnyActiveGateway(ctx context.Context) (domain.Gateway, error) {
	list, err := s.gateways.ListActive(ctx)
	if err != nil || len(list) == 0 {
		return domain.Gateway{}, domain.ErrUnavailable("no active gateway available")
	}
	for _, g := range list {
		if s.gatewayUsable(g) {
			return g, nil
		}
	}
	return domain.Gateway{}, domain.ErrUnavailable("no active gateway available")
}

// gatewayUsable derives reachability from registry state rather than trusting the
// status column alone. Active and draining gateways remain eligible only with a
// present heartbeat inside the freshness window; timestamps equally far in the
// future are rejected because clock corruption could otherwise keep a dead
// gateway routable indefinitely.
func (s *Server) gatewayUsable(g domain.Gateway) bool {
	switch g.Status {
	case domain.GatewayActive, domain.GatewayDraining:
	default:
		return false
	}
	if s.staleAfter > 0 {
		if g.LastSeenAt == nil {
			return false
		}
		age := s.now().Sub(time.UnixMilli(*g.LastSeenAt))
		if age > s.staleAfter || age < -s.staleAfter {
			return false
		}
	}
	return true
}
