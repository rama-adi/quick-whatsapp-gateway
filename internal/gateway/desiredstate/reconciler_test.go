package desiredstate

import (
	"context"
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

func TestApplyStopsRemovedAndExpiresLease(t *testing.T) {
	now := time.Unix(100, 0)
	rt := &fakeRuntime{inventory: Inventory{PairedJIDs: []string{"a"}}}
	r := New(rt, func() time.Time { return now })
	if _, err := r.Apply(context.Background(), Snapshot{Revision: 1, Assignments: []Assignment{assignment("one", "a", 1, now.Add(time.Minute))}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(context.Background(), Snapshot{Revision: 2}); err != nil {
		t.Fatal(err)
	}
	if len(rt.stopped) != 1 || rt.stopped[0] != "one" {
		t.Fatalf("stopped = %#v", rt.stopped)
	}
	if _, err := r.Apply(context.Background(), Snapshot{Revision: 3, Assignments: []Assignment{assignment("two", "a", 4, now.Add(time.Second))}}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if _, err := r.Expire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rt.stopped) != 2 || rt.stopped[1] != "two" {
		t.Fatalf("stopped=%#v", rt.stopped)
	}
	if _, ok := r.CurrentEpoch("two"); ok {
		t.Fatal("expired assignment retains epoch")
	}
}

func TestAssignmentCountTracksAppliedAuthoritativeSnapshot(t *testing.T) {
	now := time.Unix(100, 0)
	r := New(&fakeRuntime{}, func() time.Time { return now })
	if got := r.AssignmentCount(); got != 0 {
		t.Fatalf("initial assignments = %d", got)
	}
	if _, err := r.Apply(context.Background(), Snapshot{Revision: 1, Assignments: []Assignment{
		assignment("one", "one", 1, now.Add(time.Minute)),
		assignment("two", "two", 2, now.Add(time.Minute)),
	}}); err != nil {
		t.Fatal(err)
	}
	if got := r.AssignmentCount(); got != 2 {
		t.Fatalf("applied assignments = %d", got)
	}
	if _, err := r.Apply(context.Background(), Snapshot{Revision: 2}); err != nil {
		t.Fatal(err)
	}
	if got := r.AssignmentCount(); got != 0 {
		t.Fatalf("removed assignments = %d", got)
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
