package desiredstate

import (
	"context"
	"testing"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
)

type fakeTimer struct {
	stopped bool
	fire    func()
}

func (t *fakeTimer) Stop() bool { t.stopped = true; return true }

func TestControlApplierExpiresLeaseAndRenewalResetsTimer(t *testing.T) {
	now := time.Unix(100, 0)
	runtime := &fakeRuntime{inventory: Inventory{PairedJIDs: []string{"a"}}}
	reconciler := New(runtime, func() time.Time { return now })
	var timers []*fakeTimer
	expired := 0
	applier := &ControlApplier{Reconciler: reconciler, Now: func() time.Time { return now }, AfterFunc: func(_ time.Duration, fire func()) Timer {
		timer := &fakeTimer{fire: fire}
		timers = append(timers, timer)
		return timer
	}, OnLeaseExpired: func(err error) {
		if err != nil {
			t.Error(err)
		}
		expired++
	}}
	jid := "a"
	snapshot := func(lease time.Time) *gatewayv1.DesiredStateSnapshot {
		return &gatewayv1.DesiredStateSnapshot{Revision: 1, Assignments: []*gatewayv1.SessionAssignment{{SessionId: "one", OrganizationId: "org", AssignmentEpoch: 1, DeviceJid: &jid, DesiredAction: gatewayv1.SessionDesiredAction_SESSION_DESIRED_ACTION_RUN, LeaseExpiresAtUnixMs: lease.UnixMilli()}}}
	}
	if _, err := applier.ApplyDesiredState(context.Background(), 7, snapshot(now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if len(timers) != 1 {
		t.Fatalf("timers = %d", len(timers))
	}
	if _, err := applier.ApplyDesiredState(context.Background(), 7, snapshot(now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if !timers[0].stopped || len(timers) != 2 {
		t.Fatalf("timer reset = %#v", timers)
	}
	now = now.Add(2 * time.Minute)
	timers[1].fire()
	if expired != 1 || len(runtime.stopped) != 1 || runtime.stopped[0] != "one" {
		t.Fatalf("expiry: expired=%d stopped=%#v", expired, runtime.stopped)
	}
	if _, ok := reconciler.CurrentEpoch("one"); ok {
		t.Fatal("expired assignment remains fenced")
	}
}
