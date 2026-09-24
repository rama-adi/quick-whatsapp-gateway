package desiredstate

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRuntime struct {
	inventory Inventory
	started   []Assignment
	stopped   []string
}

func (f *fakeRuntime) Inventory(context.Context) (Inventory, error) { return f.inventory, nil }
func (f *fakeRuntime) StartAssigned(_ context.Context, a Assignment) error {
	f.started = append(f.started, a)
	return nil
}
func (f *fakeRuntime) StopAssigned(_ context.Context, id string) error {
	f.stopped = append(f.stopped, id)
	return nil
}
func assignment(id, jid string, epoch uint64, lease time.Time) Assignment {
	return Assignment{SessionID: id, OrganizationID: "org", DeviceJID: jid, AssignmentEpoch: epoch, LeaseExpiresAt: lease, DesiredRun: true}
}

type fencingRuntime struct {
	inventory  Inventory
	reconciler *Reconciler
	startSeen  bool
	stopSeen   []bool
	startErr   error
	stopErr    error
}

func (f *fencingRuntime) Inventory(context.Context) (Inventory, error) { return f.inventory, nil }
func (f *fencingRuntime) StartAssigned(_ context.Context, a Assignment) error {
	_, present := f.reconciler.CurrentEpoch(a.SessionID)
	f.startSeen = present && f.reconciler.OwnsSession(a.OrganizationID, a.SessionID, a.AssignmentEpoch)
	if f.startErr != nil {
		return f.startErr
	}
	if !f.startSeen {
		return errors.New("candidate assignment was not published")
	}
	return nil
}
func (f *fencingRuntime) StopAssigned(_ context.Context, id string) error {
	_, present := f.reconciler.CurrentEpoch(id)
	f.stopSeen = append(f.stopSeen, present)
	if f.stopErr != nil {
		return f.stopErr
	}
	return nil
}

func TestApplyPublishesCandidateBeforeRuntimeCallback(t *testing.T) {
	now := time.Unix(100, 0)
	rt := &fencingRuntime{inventory: Inventory{PairedJIDs: []string{"a"}}}
	r := New(rt, func() time.Time { return now })
	rt.reconciler = r
	done := make(chan error, 1)
	go func() {
		_, err := r.Apply(context.Background(), Snapshot{Revision: 1, Assignments: []Assignment{assignment("one", "a", 7, now.Add(time.Minute))}})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Apply deadlocked while runtime callback queried ownership")
	}
	if !rt.startSeen {
		t.Fatal("runtime callback did not observe the published assignment")
	}
}

func TestApplyAndExpireFenceRemovedAssignmentsBeforeStopCallback(t *testing.T) {
	now := time.Unix(100, 0)
	rt := &fencingRuntime{inventory: Inventory{PairedJIDs: []string{"a"}}}
	r := New(rt, func() time.Time { return now })
	rt.reconciler = r
	if _, err := r.Apply(context.Background(), Snapshot{Revision: 1, Assignments: []Assignment{assignment("one", "a", 1, now.Add(time.Minute))}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(context.Background(), Snapshot{Revision: 2}); err != nil {
		t.Fatal(err)
	}
	if len(rt.stopSeen) != 1 || rt.stopSeen[0] {
		t.Fatalf("removed assignment visible during stop callback: %#v", rt.stopSeen)
	}
	if _, err := r.Apply(context.Background(), Snapshot{Revision: 3, Assignments: []Assignment{assignment("two", "a", 2, now.Add(time.Second))}}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if _, err := r.Expire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rt.stopSeen) != 2 || rt.stopSeen[1] {
		t.Fatalf("expired assignment visible during stop callback: %#v", rt.stopSeen)
	}
}

func TestApplyCallbackFailureFailsClosed(t *testing.T) {
	now := time.Unix(100, 0)
	rt := &fencingRuntime{inventory: Inventory{PairedJIDs: []string{"a"}}, startErr: errors.New("start failed")}
	r := New(rt, func() time.Time { return now })
	rt.reconciler = r
	if _, err := r.Apply(context.Background(), Snapshot{Revision: 1, Assignments: []Assignment{assignment("one", "a", 1, now.Add(time.Minute))}}); err == nil {
		t.Fatal("expected start failure")
	}
	if got := r.AssignmentCount(); got != 0 {
		t.Fatalf("failed apply retained %d assignments", got)
	}
	if got := r.Revision(); got != 0 {
		t.Fatalf("failed apply advanced revision to %d", got)
	}
}

type partialRuntime struct {
	inventory   Inventory
	failSession string
	started     []string
	stopped     []string
}

func (f *partialRuntime) Inventory(context.Context) (Inventory, error) { return f.inventory, nil }
func (f *partialRuntime) StartAssigned(_ context.Context, a Assignment) error {
	f.started = append(f.started, a.SessionID)
	if a.SessionID == f.failSession {
		return errors.New("start failed")
	}
	return nil
}
func (f *partialRuntime) StopAssigned(_ context.Context, id string) error {
	f.stopped = append(f.stopped, id)
	return nil
}

func TestApplyFailureStopsPriorAndCandidateRuntimes(t *testing.T) {
	now := time.Unix(100, 0)
	rt := &partialRuntime{inventory: Inventory{PairedJIDs: []string{"a", "b"}}, failSession: "two"}
	r := New(rt, func() time.Time { return now })
	_, err := r.Apply(context.Background(), Snapshot{Revision: 1, Assignments: []Assignment{
		assignment("one", "a", 1, now.Add(time.Minute)),
		assignment("two", "b", 2, now.Add(time.Minute)),
	}})
	if err == nil {
		t.Fatal("expected partial start failure")
	}
	seen := map[string]bool{}
	for _, id := range rt.stopped {
		seen[id] = true
	}
	if !seen["one"] || !seen["two"] {
		t.Fatalf("cleanup stopped=%#v, want both candidate runtimes", rt.stopped)
	}
}

func TestExpireAttemptsEveryExpiredStopOnError(t *testing.T) {
	now := time.Unix(100, 0)
	rt := &fencingRuntime{inventory: Inventory{PairedJIDs: []string{"a", "b"}}, stopErr: errors.New("stop failed")}
	r := New(rt, func() time.Time { return now })
	rt.reconciler = r
	if _, err := r.Apply(context.Background(), Snapshot{Revision: 1, Assignments: []Assignment{
		assignment("one", "a", 1, now.Add(time.Second)),
		assignment("two", "b", 2, now.Add(time.Second)),
	}}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if _, err := r.Expire(context.Background()); err == nil {
		t.Fatal("expected stop failure")
	}
	if len(rt.stopSeen) != 2 {
		t.Fatalf("stop attempts=%d, want all expired sessions", len(rt.stopSeen))
	}
	if got := r.AssignmentCount(); got != 0 {
		t.Fatalf("failed expiry retained %d assignments", got)
	}
}

func TestApplyStartsOnlyAssignedLocalDevicesAndReportsInventory(t *testing.T) {
	now := time.Unix(100, 0)
	rt := &fakeRuntime{inventory: Inventory{PairedJIDs: []string{"a@s.whatsapp.net", "orphan@s.whatsapp.net"}, CorruptJIDs: []string{"broken@s.whatsapp.net"}}}
	r := New(rt, func() time.Time { return now })
	result, err := r.Apply(context.Background(), Snapshot{Revision: 1, Assignments: []Assignment{assignment("one", "a@s.whatsapp.net", 2, now.Add(time.Minute)), assignment("missing", "missing@s.whatsapp.net", 3, now.Add(time.Minute))}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.started) != 1 || rt.started[0].SessionID != "one" {
		t.Fatalf("started = %#v", rt.started)
	}
	if got, want := result.MissingDeviceJIDs, []string{"missing@s.whatsapp.net"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("missing = %#v", got)
	}
	if got := result.UnexpectedJIDs; len(got) != 1 || got[0] != "orphan@s.whatsapp.net" {
		t.Fatalf("unexpected = %#v", got)
	}
	if got := result.CorruptJIDs; len(got) != 1 || got[0] != "broken@s.whatsapp.net" {
		t.Fatalf("corrupt = %#v", got)
	}
	if epoch, ok := r.CurrentEpoch("one"); !ok || epoch != 2 {
		t.Fatalf("epoch = %d, %v", epoch, ok)
	}
}

func TestEqualRevisionRenewsLeaseWithoutRestart(t *testing.T) {
	now := time.Unix(100, 0)
	rt := &fakeRuntime{inventory: Inventory{PairedJIDs: []string{"a"}}}
	r := New(rt, func() time.Time { return now })
	first := assignment("one", "a", 1, now.Add(time.Second))
	if _, err := r.Apply(context.Background(), Snapshot{Revision: 1, Assignments: []Assignment{first}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(context.Background(), Snapshot{Revision: 1, Assignments: []Assignment{assignment("one", "a", 1, now.Add(time.Minute))}}); err != nil {
		t.Fatal(err)
	}
	if len(rt.started) != 2 {
		t.Fatalf("renewal did not reapply assignment metadata: %#v", rt.started)
	}
	now = now.Add(2 * time.Second)
	if _, err := r.Expire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rt.stopped) != 0 {
		t.Fatalf("renewed lease expired: %#v", rt.stopped)
	}
}

func TestEqualRevisionRechecksMissingDeviceAndStartsWhenItAppears(t *testing.T) {
	now := time.Unix(100, 0)
	rt := &fakeRuntime{}
	r := New(rt, func() time.Time { return now })
	snapshot := Snapshot{Revision: 1, Assignments: []Assignment{assignment("one", "a", 1, now.Add(time.Minute))}}
	first, err := r.Apply(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if first.Healthy || len(first.MissingDeviceJIDs) != 1 {
		t.Fatalf("first = %#v", first)
	}
	rt.inventory.PairedJIDs = []string{"a"}
	second, err := r.Apply(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Healthy || len(rt.started) != 1 || rt.started[0].SessionID != "one" {
		t.Fatalf("second=%#v starts=%#v", second, rt.started)
	}
}

func TestApplyRejectsInvalidFence(t *testing.T) {
	r := New(&fakeRuntime{}, time.Now)
	for _, s := range []Snapshot{{}, {Revision: 1, Assignments: []Assignment{assignment("", "jid", 1, time.Now())}}, {Revision: 1, Assignments: []Assignment{assignment("one", "jid", 0, time.Now())}}} {
		if _, err := r.Apply(context.Background(), s); err != ErrInvalidSnapshot {
			t.Fatalf("err=%v", err)
		}
	}
}
