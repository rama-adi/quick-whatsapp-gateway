// Package desiredstate reconciles the API's complete assignment snapshot with
// devices held by this gateway. It deliberately has no database dependency:
// the control plane is authoritative for boot ownership.
package desiredstate

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrInvalidSnapshot = errors.New("desired state: invalid snapshot")

type Config struct {
	Revision       uint64
	AutoRead       bool
	PresenceTyping bool
	RatePerMin     uint32
	RatePerHour    uint32
}

type Assignment struct {
	SessionID       string
	OrganizationID  string
	DeviceJID       string
	AssignmentEpoch uint64
	LeaseExpiresAt  time.Time
	DesiredRun      bool
	Config          Config
}

type Snapshot struct {
	Revision    uint64
	Assignments []Assignment
}

type Inventory struct {
	PairedJIDs  []string
	CorruptJIDs []string
}

type Result struct {
	Revision          uint64
	Healthy           bool
	MissingDeviceJIDs []string
	UnexpectedJIDs    []string
	CorruptJIDs       []string
	LocalDeviceJIDs   []string
}

// Runtime is the gateway-local session engine. Start must use only the supplied
// assignment metadata; it must not look up wa_sessions or organizations.
type Runtime interface {
	Inventory(context.Context) (Inventory, error)
	StartAssigned(context.Context, Assignment) error
	StopAssigned(context.Context, string) error
}

type Reconciler struct {
	runtime Runtime
	now     func() time.Time

	mu          sync.RWMutex
	revision    uint64
	assignments map[string]Assignment
}

func New(runtime Runtime, now func() time.Time) *Reconciler {
	if now == nil {
		now = time.Now
	}
	return &Reconciler{runtime: runtime, now: now, assignments: make(map[string]Assignment)}
}

// Apply atomically advances the desired revision after all locally applicable
// assignments have been reconciled. Stale and duplicate revisions are harmless
// no-ops, which is required for stream replay after reconnect.
func (r *Reconciler) Apply(ctx context.Context, snapshot Snapshot) (Result, error) {
	if r.runtime == nil {
		return Result{}, errors.New("desired state: nil runtime")
	}
	if err := validate(snapshot); err != nil {
		return Result{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if snapshot.Revision < r.revision {
		return Result{Revision: r.revision}, nil
	}

	inventory, err := r.runtime.Inventory(ctx)
	if err != nil {
		return Result{}, err
	}
	paired := make(map[string]struct{}, len(inventory.PairedJIDs))
	for _, jid := range inventory.PairedJIDs {
		paired[jid] = struct{}{}
	}
	next := make(map[string]Assignment, len(snapshot.Assignments))
	wantedDevices := make(map[string]struct{}, len(snapshot.Assignments))
	result := Result{Revision: snapshot.Revision, Healthy: true, CorruptJIDs: sorted(inventory.CorruptJIDs), LocalDeviceJIDs: sorted(append([]string(nil), inventory.PairedJIDs...))}
	now := r.now()
	for _, assignment := range snapshot.Assignments {
		if assignment.DeviceJID != "" {
			wantedDevices[assignment.DeviceJID] = struct{}{}
		}
		if !assignment.DesiredRun {
			next[assignment.SessionID] = assignment
			if _, active := r.assignments[assignment.SessionID]; active {
				if err := r.runtime.StopAssigned(ctx, assignment.SessionID); err != nil {
					return Result{}, err
				}
			}
			continue
		}
		if !assignment.LeaseExpiresAt.After(now) {
			if _, active := r.assignments[assignment.SessionID]; active {
				if err := r.runtime.StopAssigned(ctx, assignment.SessionID); err != nil {
					return Result{}, err
				}
			}
			continue
		}
		next[assignment.SessionID] = assignment
		if _, found := paired[assignment.DeviceJID]; found {
			// Runtime start is idempotent. Calling it every snapshot both admits a
			// device that appeared since the previous report and refreshes config.
			if err := r.runtime.StartAssigned(ctx, assignment); err != nil {
				return Result{}, err
			}
		} else {
			result.MissingDeviceJIDs = append(result.MissingDeviceJIDs, assignment.DeviceJID)
		}
	}
	for id := range r.assignments {
		if _, retained := next[id]; !retained {
			if err := r.runtime.StopAssigned(ctx, id); err != nil {
				return Result{}, err
			}
		}
	}
	for jid := range paired {
		if _, expected := wantedDevices[jid]; !expected {
			result.UnexpectedJIDs = append(result.UnexpectedJIDs, jid)
		}
	}
	result.MissingDeviceJIDs = sorted(result.MissingDeviceJIDs)
	result.UnexpectedJIDs = sorted(result.UnexpectedJIDs)
	result.Healthy = len(result.MissingDeviceJIDs) == 0 && len(result.UnexpectedJIDs) == 0 && len(result.CorruptJIDs) == 0
	r.assignments, r.revision = next, snapshot.Revision
	return result, nil
}

// Expire stops assignments whose API-clock lease has elapsed. The caller owns
// scheduling; a stale control connection must invoke this even with no frames.
func (r *Reconciler) Expire(ctx context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	expired := false
	for id, assignment := range r.assignments {
		if !assignment.LeaseExpiresAt.After(now) {
			if err := r.runtime.StopAssigned(ctx, id); err != nil {
				return false, err
			}
			expired = true
			delete(r.assignments, id)
		}
	}
	return expired, nil
}

func (r *Reconciler) CurrentEpoch(sessionID string) (uint64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.assignments[sessionID]
	return a.AssignmentEpoch, ok
}

// AllowsMutation proves that the session remains locally assigned at exactly
// epoch with a live lease. Engine commands must check this immediately before
// dispatching a WhatsApp side effect.
func (r *Reconciler) AllowsMutation(organizationID, sessionID string, epoch uint64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.assignments[sessionID]
	return ok && a.OrganizationID == organizationID && a.DesiredRun && a.AssignmentEpoch == epoch && a.LeaseExpiresAt.After(r.now())
}

// OwnsSession is the read-side ownership check for gateway-local live state.
func (r *Reconciler) OwnsSession(organizationID, sessionID string, epoch uint64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.assignments[sessionID]
	return ok && a.OrganizationID == organizationID && a.DesiredRun && a.AssignmentEpoch == epoch && a.LeaseExpiresAt.After(r.now())
}

func (r *Reconciler) Revision() uint64 { r.mu.RLock(); defer r.mu.RUnlock(); return r.revision }

// AssignmentCount returns the current authoritative assignment set after the
// last successfully applied snapshot. It is safe for heartbeat reporting and
// deliberately does not consult application persistence.
func (r *Reconciler) AssignmentCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.assignments)
}

func (r *Reconciler) NextLeaseExpiry() (time.Time, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var earliest time.Time
	for _, assignment := range r.assignments {
		if !assignment.DesiredRun {
			continue
		}
		if earliest.IsZero() || assignment.LeaseExpiresAt.Before(earliest) {
			earliest = assignment.LeaseExpiresAt
		}
	}
	return earliest, !earliest.IsZero()
}

func validate(snapshot Snapshot) error {
	if snapshot.Revision == 0 {
		return ErrInvalidSnapshot
	}
	seenSessions, seenDevices := map[string]struct{}{}, map[string]struct{}{}
	for _, a := range snapshot.Assignments {
		if a.SessionID == "" || a.OrganizationID == "" || (a.DesiredRun && a.DeviceJID == "") || a.AssignmentEpoch == 0 || a.LeaseExpiresAt.IsZero() {
			return ErrInvalidSnapshot
		}
		if _, exists := seenSessions[a.SessionID]; exists {
			return ErrInvalidSnapshot
		}
		seenSessions[a.SessionID] = struct{}{}
		if a.DeviceJID == "" {
			continue
		}
		if _, exists := seenDevices[a.DeviceJID]; exists {
			return ErrInvalidSnapshot
		}
		seenDevices[a.DeviceJID] = struct{}{}
	}
	return nil
}

func sorted(values []string) []string { sort.Strings(values); return values }
