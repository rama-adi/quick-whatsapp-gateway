package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/gateway/controlsupervisor"
)

type fixedControlStatus struct{ status controlsupervisor.Status }

func (s fixedControlStatus) Status() controlsupervisor.Status { return s.status }

func TestControlReadinessFailsBeforeInfrastructureChecks(t *testing.T) {
	probe := readiness(nil, nil, fixedControlStatus{status: controlsupervisor.Status{
		Connected:        true,
		Ready:            false,
		DesiredLifecycle: gatewayv1.LifecycleDirectiveAction_LIFECYCLE_DIRECTIVE_ACTION_DRAIN,
	}})
	if err := probe(); err == nil {
		t.Fatal("DRAIN control state reported ready")
	}
}

type countedWorkerServer struct {
	starts    atomic.Int32
	shutdowns atomic.Int32
}

func (s *countedWorkerServer) Start() error {
	s.starts.Add(1)
	return nil
}

func (s *countedWorkerServer) Shutdown() { s.shutdowns.Add(1) }

func TestAdmittedWorkersCannotStartAfterDrain(t *testing.T) {
	server := &countedWorkerServer{}
	ready := atomic.Bool{}
	workers := newAdmittedWorkers(server, ready.Load)

	// Model delayed control admission: startup is queued while unready, then a
	// drain wins before the acknowledged READY notification is observed.
	if err := workers.Start(); err != nil {
		t.Fatal(err)
	}
	workers.Drain()
	ready.Store(true)

	const attempts = 32
	done := make(chan struct{}, attempts)
	for range attempts {
		go func() {
			_ = workers.Start()
			done <- struct{}{}
		}()
	}
	for range attempts {
		<-done
	}
	if starts := server.starts.Load(); starts != 0 {
		t.Fatalf("terminally drained workers started %d times", starts)
	}
	if shutdowns := server.shutdowns.Load(); shutdowns != 0 {
		t.Fatalf("never-started workers shut down %d times", shutdowns)
	}
}

func TestAdmittedWorkersStartOnceAndDrainOnce(t *testing.T) {
	server := &countedWorkerServer{}
	workers := newAdmittedWorkers(server, func() bool { return true })
	if err := workers.Start(); err != nil {
		t.Fatal(err)
	}
	if err := workers.Start(); err != nil {
		t.Fatal(err)
	}
	workers.Drain()
	workers.Drain()
	if server.starts.Load() != 1 || server.shutdowns.Load() != 1 {
		t.Fatalf("starts=%d shutdowns=%d", server.starts.Load(), server.shutdowns.Load())
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

// The legacy registry has five mutation sites. When the control plane is
// configured, none may execute: authenticated control-stream writes own them.
func TestEveryLegacyRegistryMutationIsControlGated(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, "main.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var guarded []struct{ start, end token.Pos }
	ast.Inspect(file, func(node ast.Node) bool {
		statement, ok := node.(*ast.IfStmt)
		if !ok {
			return true
		}
		unary, ok := statement.Cond.(*ast.UnaryExpr)
		if !ok {
			return true
		}
		identifier, identifierOK := unary.X.(*ast.Ident)
		if identifierOK && unary.Op == token.NOT && identifier.Name == "controlEnabled" {
			guarded = append(guarded, struct{ start, end token.Pos }{statement.Body.Pos(), statement.Body.End()})
		}
		return true
	})
	targets := map[string]int{"registerGateway": 0, "startGatewayHeartbeat": 0, "SetStatus": 0}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch function := call.Fun.(type) {
		case *ast.Ident:
			name = function.Name
		case *ast.SelectorExpr:
			name = function.Sel.Name
		}
		if _, target := targets[name]; !target {
			return true
		}
		targets[name]++
		for _, region := range guarded {
			if call.Pos() >= region.start && call.End() <= region.end {
				return true
			}
		}
		t.Errorf("%s mutation at %s is not gated by !controlEnabled", name, files.Position(call.Pos()))
		return true
	})
	if targets["registerGateway"] != 2 || targets["startGatewayHeartbeat"] != 1 || targets["SetStatus"] != 2 {
		t.Fatalf("legacy registry mutation sites changed: %#v", targets)
	}
}
