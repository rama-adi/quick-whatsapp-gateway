package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"testing"
	"time"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/controlsupervisor"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/desiredstate"
)

type fixedControlStatus struct{ status controlsupervisor.Status }

func (s fixedControlStatus) Status() controlsupervisor.Status { return s.status }

func TestControlReadinessFailsBeforeInfrastructureChecks(t *testing.T) {
	probe := readiness(fixedControlStatus{status: controlsupervisor.Status{
		Connected:        true,
		Ready:            false,
		DesiredLifecycle: gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN,
	}}, nil)
	if err := probe(); err == nil {
		t.Fatal("DRAIN control state reported ready")
	}
}

func TestGatewayControlRuntimeSnapshot(t *testing.T) {
	runtime := newGatewayControlRuntime(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if snapshot := runtime.Snapshot(); snapshot.State != gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_STARTING || snapshot.SessionCount != 0 {
		t.Fatalf("initial snapshot = %#v", snapshot)
	}
	runtime.setSessionCounter(func(context.Context) (int, error) { return 4, nil })
	runtime.setState(gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY)
	snapshot := runtime.Snapshot()
	if snapshot.State != gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY || snapshot.SessionCount != 4 {
		t.Fatalf("ready snapshot = %#v", snapshot)
	}
}

func TestGatewayControlRuntimeCountsAppliedAssignmentsWithoutRepository(t *testing.T) {
	now := time.Unix(100, 0)
	reconciler := desiredstate.New(&controlCountRuntime{}, func() time.Time { return now })
	if _, err := reconciler.Apply(context.Background(), desiredstate.Snapshot{Revision: 1, Assignments: []desiredstate.Assignment{
		{SessionID: "one", OrganizationID: "org", DeviceJID: "one", AssignmentEpoch: 1, LeaseExpiresAt: now.Add(time.Minute), DesiredRun: true},
		{SessionID: "two", OrganizationID: "org", DeviceJID: "two", AssignmentEpoch: 2, LeaseExpiresAt: now.Add(time.Minute), DesiredRun: true},
	}}); err != nil {
		t.Fatal(err)
	}
	runtime := newGatewayControlRuntime(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	runtime.setSessionCounter(func(context.Context) (int, error) { return reconciler.AssignmentCount(), nil })
	if snapshot := runtime.Snapshot(); snapshot.SessionCount != 2 {
		t.Fatalf("control heartbeat session count = %d", snapshot.SessionCount)
	}
}

type controlCountRuntime struct{}

func (*controlCountRuntime) Inventory(context.Context) (desiredstate.Inventory, error) {
	return desiredstate.Inventory{PairedJIDs: []string{"one", "two"}}, nil
}
func (*controlCountRuntime) StartAssigned(context.Context, desiredstate.Assignment) error { return nil }
func (*controlCountRuntime) StopAssigned(context.Context, string) error                   { return nil }

func TestGatewayInstanceIDIsProcessUnique(t *testing.T) {
	first, err := newGatewayInstanceID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newGatewayInstanceID()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 || len(second) != 32 || first == second {
		t.Fatalf("instance ids %q and %q", first, second)
	}
}

func TestCertificateRenewalDeadlineUsesCertificateExpiryAndConfiguredWindow(t *testing.T) {
	expiry := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	if got, want := renewalDeadline(expiry, 90*time.Minute), expiry.Add(-90*time.Minute); !got.Equal(want) {
		t.Fatalf("renewal deadline = %s, want %s", got, want)
	}
}

// The legacy registry (registerGateway / heartbeat / SetStatus writes) was
// deleted with the gateway's MySQL access. None of its mutation sites may
// reappear in the composition root.
func TestGatewayMainHasNoLegacyRegistryMutations(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, "main.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{"registerGateway": true, "startGatewayHeartbeat": true}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && forbidden[ident.Name] {
			t.Errorf("legacy registry mutation %s reappeared at %s", ident.Name, files.Position(call.Pos()))
		}
		return true
	})
}
