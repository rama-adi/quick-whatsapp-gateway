package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/desiredstate"
	wastore "github.com/rama-adi/quick-whatsapp-gateway/internal/wa/store"
	sqlitestore "github.com/rama-adi/quick-whatsapp-gateway/internal/wa/store/sqlite"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

var _ wastore.Keystore = (*switchableKeystore)(nil)

func TestControlKeystoreMissingStaysInertUntilEmptyBootstrap(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "keystore.db")
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	runtime, err := openControlKeystore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if state := runtime.Health().GetState(); state != gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_MISSING {
		t.Fatalf("health state = %s", state)
	}
	devices, err := runtime.holder.GetAllDevices(ctx)
	if err != nil || len(devices) != 0 {
		t.Fatalf("inert device inventory = %#v, %v", devices, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing store was created before authoritative bootstrap: %v", err)
	}

	if err := runtime.BootstrapEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	if state := runtime.Health().GetState(); state != gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY {
		t.Fatalf("bootstrap health state = %s", state)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("second close = %v", err)
	}
}

func TestInertKeystoreFailsClosedOutsideInventory(t *testing.T) {
	holder := &switchableKeystore{}
	ctx := context.Background()
	if devices, err := holder.GetAllDevices(ctx); err != nil || len(devices) != 0 {
		t.Fatalf("inventory = %#v, %v", devices, err)
	}
	if _, err := holder.GetFirstDevice(ctx); !errors.Is(err, errKeystoreUnavailable) {
		t.Fatalf("first device error = %v", err)
	}
	if _, err := holder.GetDevice(ctx, types.NewJID("1", types.DefaultUserServer)); !errors.Is(err, errKeystoreUnavailable) {
		t.Fatalf("get device error = %v", err)
	}
	if device := holder.NewDevice(); device != nil {
		t.Fatalf("new device = %#v", device)
	}
	if err := holder.PutDevice(ctx, &store.Device{}); !errors.Is(err, errKeystoreUnavailable) {
		t.Fatalf("put device error = %v", err)
	}
	if err := holder.DeleteDevice(ctx, &store.Device{}); !errors.Is(err, errKeystoreUnavailable) {
		t.Fatalf("delete device error = %v", err)
	}
}

func TestControlKeystoreCorruptNeverBootstraps(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "keystore.db")
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := openControlKeystore(ctx, "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if state := runtime.Health().GetState(); state != gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_CORRUPT {
		t.Fatalf("health state = %s", state)
	}
	if err := runtime.BootstrapEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	if state := runtime.Health().GetState(); state != gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_CORRUPT {
		t.Fatalf("corrupt store was reinitialized: %s", state)
	}
	health, err := sqlitestore.Inspect(ctx, "file:"+path+"?_pragma=foreign_keys(1)")
	if !errors.Is(err, sqlitestore.ErrKeystoreCorrupt) || health.Integrity != "corrupt" {
		t.Fatalf("corrupt store changed: health=%#v err=%v", health, err)
	}
}

func TestBootstrapControlApplierRequiresAnAuthoritativeEmptySnapshot(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "keystore.db") + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	runtime, err := openControlKeystore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	delegate := &desiredstate.ControlApplier{Reconciler: desiredstate.New(&emptyRuntime{}, nil), Health: runtime.Health}
	applier := &bootstrapControlApplier{delegate: delegate, keystore: runtime}
	jid := "123@s.whatsapp.net"
	_, err = applier.ApplyDesiredState(ctx, 1, &gatewayv1.DesiredStateSnapshot{Revision: 1, Assignments: []*gatewayv1.SessionAssignment{{SessionId: "session", OrganizationId: "org", DeviceJid: &jid, AssignmentEpoch: 1, LeaseExpiresAtUnixMs: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if state := runtime.Health().GetState(); state != gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_MISSING {
		t.Fatalf("assigned snapshot initialized missing store: %s", state)
	}
	_, err = applier.ApplyDesiredState(ctx, 1, &gatewayv1.DesiredStateSnapshot{Revision: 2})
	if err != nil {
		t.Fatal(err)
	}
	if state := runtime.Health().GetState(); state != gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY {
		t.Fatalf("empty authoritative snapshot did not initialize store: %s", state)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestControlRuntimeStateRequiresHealthyReconciliation(t *testing.T) {
	runtime := &gatewayKeystoreRuntime{health: &gatewayv1.KeystoreHealth{State: gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY}}
	if state := runtime.RuntimeState(); state != gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED {
		t.Fatalf("unreconciled state = %s", state)
	}
	runtime.setReconciliation(&gatewayv1.DesiredStateReport{KeystoreHealth: &gatewayv1.KeystoreHealth{State: gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY}})
	if state := runtime.RuntimeState(); state != gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY {
		t.Fatalf("healthy state = %s", state)
	}
	runtime.setReconciliation(&gatewayv1.DesiredStateReport{KeystoreHealth: &gatewayv1.KeystoreHealth{State: gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY}, Results: []*gatewayv1.ReconciliationResult{{Status: gatewayv1.ReconciliationResultStatus_RECONCILIATION_RESULT_STATUS_KEYSTORE_MISSING}}})
	if state := runtime.RuntimeState(); state != gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED {
		t.Fatalf("missing reconciliation state = %s", state)
	}
}

type emptyRuntime struct{}

func (*emptyRuntime) Inventory(context.Context) (desiredstate.Inventory, error) {
	return desiredstate.Inventory{}, nil
}
func (*emptyRuntime) StartAssigned(context.Context, desiredstate.Assignment) error { return nil }
func (*emptyRuntime) StopAssigned(context.Context, string) error                   { return nil }
