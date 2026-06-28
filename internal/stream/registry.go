package stream

import (
	"context"
	"sync"
	"sync/atomic"
)

// ConnIdentity is the authenticated identity of a live NDJSON connection, used by
// the control bus to drop streams precisely on revocation (§4.6). Any field may
// be empty: api-key streams carry KeyID + OrganizationID (no UserID); JWT streams
// carry UserID + OrganizationID (no KeyID).
type ConnIdentity struct {
	KeyID          string
	UserID         string
	OrganizationID string
}

// PrincipalAccessor lifts a connection's ConnIdentity off the request context.
// It is a consumer interface so the stream package stays free of the authz import;
// the router supplies a func adapter. A nil accessor (or one returning ok=false)
// means the connection is registered with only the organization id the caller
// already resolves.
type PrincipalAccessor interface {
	IdentityFromContext(ctx context.Context) (ConnIdentity, bool)
}

// PrincipalAccessorFunc adapts a plain function to PrincipalAccessor.
type PrincipalAccessorFunc func(ctx context.Context) (ConnIdentity, bool)

func (f PrincipalAccessorFunc) IdentityFromContext(ctx context.Context) (ConnIdentity, bool) {
	return f(ctx)
}

// ConnRegistry tracks live NDJSON connections and can drop them by key/user/org.
// It is the StreamDropper the control bus calls when a key is revoked or a user
// banned/removed. Dropping a connection cancels its request context, which makes
// ServeHTTP return and close the stream; a reconnect re-validates against MySQL
// (the row is gone) and fails closed.
type ConnRegistry struct {
	mu    sync.Mutex
	next  uint64
	conns map[uint64]*registeredConn
}

type registeredConn struct {
	id     ConnIdentity
	cancel context.CancelFunc
}

// NewConnRegistry constructs an empty registry.
func NewConnRegistry() *ConnRegistry {
	return &ConnRegistry{conns: make(map[uint64]*registeredConn)}
}

// Register adds a connection and returns a deregister func the caller defers. It
// is the exported entry point used by the router's WebSocket realtime handler;
// the in-package NDJSON handler uses the unexported alias.
func (r *ConnRegistry) Register(id ConnIdentity, cancel context.CancelFunc) func() {
	return r.register(id, cancel)
}

// register adds a connection and returns a deregister func the handler defers.
func (r *ConnRegistry) register(id ConnIdentity, cancel context.CancelFunc) func() {
	h := atomic.AddUint64(&r.next, 1)
	r.mu.Lock()
	r.conns[h] = &registeredConn{id: id, cancel: cancel}
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.conns, h)
		r.mu.Unlock()
	}
}

// DropByKey cancels every live connection authenticated by the given api-key id.
func (r *ConnRegistry) DropByKey(keyID string) int {
	if keyID == "" {
		return 0
	}
	return r.dropMatching(func(id ConnIdentity) bool { return id.KeyID == keyID })
}

// DropByUser cancels every live connection authenticated by the given user.
func (r *ConnRegistry) DropByUser(userID string) int {
	if userID == "" {
		return 0
	}
	return r.dropMatching(func(id ConnIdentity) bool { return id.UserID == userID })
}

// DropByUserOrg cancels live connections for a user within one organization
// (member.removed): the user keeps access to their other orgs' streams.
func (r *ConnRegistry) DropByUserOrg(userID, orgID string) int {
	if userID == "" || orgID == "" {
		return 0
	}
	return r.dropMatching(func(id ConnIdentity) bool {
		return id.UserID == userID && id.OrganizationID == orgID
	})
}

// dropMatching cancels (and forgets) every connection whose identity matches pred,
// returning how many were dropped. The deferred deregister in the handler also
// runs as each ServeHTTP unwinds, so removing here is belt-and-suspenders.
func (r *ConnRegistry) dropMatching(pred func(ConnIdentity) bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for h, c := range r.conns {
		if pred(c.id) {
			c.cancel()
			delete(r.conns, h)
			n++
		}
	}
	return n
}
