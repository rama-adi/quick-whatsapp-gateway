package desiredstate

import (
	"context"
	"testing"
	"time"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
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

// TestControlReportCarriesSessionRuntime: every report carries the observed
// runtime of each assigned session, so the API repairs a status event that
// never reached it within one heartbeat.
func TestControlReportCarriesSessionRuntime(t *testing.T) {
	now := time.Unix(100, 0)
	runtime := &fakeRuntime{inventory: Inventory{}}
	applier := &ControlApplier{Reconciler: New(runtime, func() time.Time { return now }), Now: func() time.Time { return now },
		AfterFunc: func(time.Duration, func()) Timer { return &fakeTimer{} }}
	report, err := applier.ApplyDesiredState(context.Background(), 7, &gatewayv1.DesiredStateSnapshot{Revision: 3, Assignments: []*gatewayv1.SessionAssignment{{
		SessionId: "one", OrganizationId: "org", AssignmentEpoch: 2, StartGeneration: 5,
		DesiredAction: gatewayv1.SessionDesiredAction_SESSION_DESIRED_ACTION_STOP, LeaseExpiresAtUnixMs: now.Add(time.Minute).UnixMilli(),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Sessions) != 1 {
		t.Fatalf("report sessions = %+v", report.Sessions)
	}
	got := report.Sessions[0]
	if got.SessionId != "one" || got.AssignmentEpoch != 2 || got.StatusSeq != 1 ||
		got.Status != gatewayv1.GatewaySessionStatus_GATEWAY_SESSION_STATUS_STOPPED {
		t.Fatalf("session runtime = %+v", got)
	}
}

// TestControlRunWithoutDeviceStartsPairing: a RUN assignment without a device
// asks the runtime to pair, and the device the session paired before the API
// attached it is reported as the session's, not as an unexpected device.
func TestControlRunWithoutDeviceStartsPairing(t *testing.T) {
	now := time.Unix(100, 0)
	runtime := &fakeRuntime{inventory: Inventory{
		PairedJIDs:     []string{"new@s.whatsapp.net"},
		SessionDevices: map[string]string{"one": "new@s.whatsapp.net"},
	}}
	applier := &ControlApplier{Reconciler: New(runtime, func() time.Time { return now }), Now: func() time.Time { return now },
		AfterFunc: func(time.Duration, func()) Timer { return &fakeTimer{} }}
	report, err := applier.ApplyDesiredState(context.Background(), 7, &gatewayv1.DesiredStateSnapshot{Revision: 3, Assignments: []*gatewayv1.SessionAssignment{{
		SessionId: "one", OrganizationId: "org", AssignmentEpoch: 2, StartGeneration: 1,
		DesiredAction: gatewayv1.SessionDesiredAction_SESSION_DESIRED_ACTION_RUN, LeaseExpiresAtUnixMs: now.Add(time.Minute).UnixMilli(),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.started) != 1 || runtime.started[0].DeviceJID != "" || runtime.started[0].StartGeneration != 1 {
		t.Fatalf("started = %+v", runtime.started)
	}
	for _, result := range report.Results {
		if result.Status == gatewayv1.ReconciliationResultStatus_RECONCILIATION_RESULT_STATUS_UNEXPECTED_LOCAL_DEVICE {
			t.Fatalf("the session's fresh pairing was reported as an unexpected device: %+v", report.Results)
		}
	}
	if len(report.Results) != 1 || report.Results[0].DeviceJid != "new@s.whatsapp.net" {
		t.Fatalf("results = %+v", report.Results)
	}
}
