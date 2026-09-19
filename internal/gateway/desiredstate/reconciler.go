// Package desiredstate reconciles the API's complete assignment snapshot with
// devices held by this gateway. It deliberately has no database dependency:
// the control plane is authoritative for boot ownership.
package desiredstate

import (
	"context"
	"errors"
	"slices"
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

	// applyMu serializes reconciliation transactions without holding mu across
	// callbacks into Runtime. Runtime status publication can synchronously ask
	// CurrentEpoch, so holding mu while starting or stopping a session deadlocks.
	applyMu     sync.Mutex
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

// Apply advances the desired revision after all locally applicable assignments
// have been reconciled. The provisional assignment set is published before
// Runtime callbacks so status/event fencing can observe the new generation;
// callbacks run without holding mu. Stale and duplicate revisions are harmless
// no-ops, which is required for stream replay after reconnect.
func (r *Reconciler) Apply(ctx context.Context, snapshot Snapshot) (Result, error) {
	if r.runtime == nil {
		return Result{}, errors.New("desired state: nil runtime")
	}
	if err := validate(snapshot); err != nil {
		return Result{}, err
	}
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	r.mu.RLock()
	if snapshot.Revision < r.revision {
		revision := r.revision
		r.mu.RUnlock()
		return Result{Revision: revision}, nil
	}
	previousRevision := r.revision
	previousAssignments := cloneAssignments(r.assignments)
	r.mu.RUnlock()

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
	result := Result{
		Revision:        snapshot.Revision,
		Healthy:         true,
		CorruptJIDs:     sorted(inventory.CorruptJIDs),
		LocalDeviceJIDs: sorted(slices.Clone(inventory.PairedJIDs)),
	}
	startAssignments := make([]Assignment, 0, len(snapshot.Assignments))
	stopIDs := make([]string, 0, len(previousAssignments))
	stopSet := make(map[string]struct{}, len(previousAssignments))
	addStop := func(id string) {
		if _, ok := stopSet[id]; ok {
			return
		}
		stopSet[id] = struct{}{}
		stopIDs = append(stopIDs, id)
	}
	now := r.now()
	for _, assignment := range snapshot.Assignments {
		if assignment.DeviceJID != "" {
			wantedDevices[assignment.DeviceJID] = struct{}{}
		}
		if !assignment.DesiredRun || !assignment.LeaseExpiresAt.After(now) {
			if _, active := previousAssignments[assignment.SessionID]; active {
				addStop(assignment.SessionID)
			}
			if assignment.DesiredRun {
				continue // expired lease: not retained, not restarted
			}
			next[assignment.SessionID] = assignment
			continue
		}
		next[assignment.SessionID] = assignment
		if _, found := paired[assignment.DeviceJID]; !found {
			result.MissingDeviceJIDs = append(result.MissingDeviceJIDs, assignment.DeviceJID)
			continue
		}
		// Runtime start is idempotent. Calling it every snapshot both admits a
		// device that appeared since the previous report and refreshes config.
		startAssignments = append(startAssignments, assignment)
	}
	for id := range previousAssignments {
		if _, retained := next[id]; !retained {
			addStop(id)
		}
	}
	for jid := range paired {
		if _, expected := wantedDevices[jid]; !expected {
			result.UnexpectedJIDs = append(result.UnexpectedJIDs, jid)
		}
	}
	result.MissingDeviceJIDs = sorted(result.MissingDeviceJIDs)
	result.UnexpectedJIDs = sorted(result.UnexpectedJIDs)
	noMissing := len(result.MissingDeviceJIDs) == 0
	noUnexpected := len(result.UnexpectedJIDs) == 0
	noCorrupt := len(result.CorruptJIDs) == 0
	result.Healthy = noMissing && noUnexpected && noCorrupt
	// Publish the candidate before callbacks. StartAssigned commonly publishes
	// status synchronously, and that callback must observe the new assignment
	// epoch. Stop callbacks may observe either the retained terminal assignment
	// or no assignment for a removed session.
	r.mu.Lock()
	r.assignments = next
	r.mu.Unlock()
	for _, id := range stopIDs {
		if err := r.runtime.StopAssigned(ctx, id); err != nil {
			return Result{}, r.rollback(ctx, previousRevision, previousAssignments, next, err)
		}
	}
	for _, assignment := range startAssignments {
		if err := r.runtime.StartAssigned(ctx, assignment); err != nil {
			return Result{}, r.rollback(ctx, previousRevision, previousAssignments, next, err)
		}
	}
	r.mu.Lock()
	r.revision = snapshot.Revision
	r.mu.Unlock()
	return result, nil
}

func (r *Reconciler) failClosed(previousRevision uint64) {
	r.mu.Lock()
	r.assignments = make(map[string]Assignment)
	r.revision = previousRevision
	r.mu.Unlock()
}

// rollback stops every runtime that could have been touched by a partially
// applied transaction. The assignment map remains published while cleanup
// runs so callbacks can still resolve the candidate generation; it is then
// cleared regardless of cleanup errors.
func (r *Reconciler) rollback(ctx context.Context, previousRevision uint64, previous, candidate map[string]Assignment, cause error) error {
	ids := make(map[string]struct{}, len(previous)+len(candidate))
	for id := range previous {
		ids[id] = struct{}{}
	}
	for id := range candidate {
		ids[id] = struct{}{}
	}
	cleanupErr := error(nil)
	for id := range ids {
		cleanupErr = errors.Join(cleanupErr, r.runtime.StopAssigned(ctx, id))
	}
	r.failClosed(previousRevision)
	return errors.Join(cause, cleanupErr)
}

func cloneAssignments(assignments map[string]Assignment) map[string]Assignment {
	clone := make(map[string]Assignment, len(assignments))
	for id, assignment := range assignments {
		clone[id] = assignment
	}
	return clone
}

// Expire stops assignments whose API-clock lease has elapsed. The caller owns
// scheduling; a stale control connection must invoke this even with no frames.
func (r *Reconciler) Expire(ctx context.Context) (bool, error) {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	now := r.now()
	r.mu.RLock()
	previousRevision := r.revision
	r.mu.RUnlock()
	r.mu.Lock()
	expiredAssignments := make([]string, 0)
	for id, assignment := range r.assignments {
		if !assignment.LeaseExpiresAt.After(now) {
			expiredAssignments = append(expiredAssignments, id)
			delete(r.assignments, id)
		}
	}
	r.mu.Unlock()
	expired := len(expiredAssignments) > 0
	var stopErr error
	for _, id := range expiredAssignments {
		if err := r.runtime.StopAssigned(ctx, id); err != nil {
			stopErr = errors.Join(stopErr, err)
		}
	}
	if stopErr != nil {
		r.failClosed(previousRevision)
		return false, stopErr
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
	return ok && assignmentActive(a, organizationID, epoch, r.now())
}

// OwnsSession is the read-side ownership check for gateway-local live state.
func (r *Reconciler) OwnsSession(organizationID, sessionID string, epoch uint64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.assignments[sessionID]
	return ok && assignmentActive(a, organizationID, epoch, r.now())
}

func assignmentActive(a Assignment, organizationID string, epoch uint64, now time.Time) bool {
	return a.OrganizationID == organizationID &&
		a.DesiredRun &&
		a.AssignmentEpoch == epoch &&
		a.LeaseExpiresAt.After(now)
}

func (r *Reconciler) Revision() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.revision
}

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
	seenSessions := make(map[string]struct{})
	seenDevices := make(map[string]struct{})
	for _, a := range snapshot.Assignments {
		invalid := a.SessionID == "" ||
			a.OrganizationID == "" ||
			(a.DesiredRun && a.DeviceJID == "") ||
			a.AssignmentEpoch == 0 ||
			a.LeaseExpiresAt.IsZero()
		if invalid {
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

func sorted(values []string) []string {
	slices.Sort(values)
	return values
}
