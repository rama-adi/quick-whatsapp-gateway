package main

import (
	"context"
	"errors"
	"sync"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/gateway/desiredstate"
	wastore "github.com/ramaadi/quick-whatsapp-gateway/internal/wa/store"
	sqlitestore "github.com/ramaadi/quick-whatsapp-gateway/internal/wa/store/sqlite"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

var errKeystoreUnavailable = errors.New("gateway keystore is unavailable pending authoritative empty bootstrap")

// switchableKeystore makes a missing control-plane keystore inert until an
// explicitly empty authoritative snapshot permits first-use bootstrap. It is
// deliberately empty rather than synthetic: assigned devices therefore report
// missing and cannot be adopted, started, or paired.
type switchableKeystore struct {
	mu       sync.RWMutex
	keystore wastore.Keystore
}

func (s *switchableKeystore) set(keystore wastore.Keystore) {
	s.mu.Lock()
	s.keystore = keystore
	s.mu.Unlock()
}

func (s *switchableKeystore) current() wastore.Keystore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.keystore
}

func (s *switchableKeystore) GetAllDevices(ctx context.Context) ([]*waStore.Device, error) {
	if keystore := s.current(); keystore != nil {
		return keystore.GetAllDevices(ctx)
	}
	return nil, nil
}

func (s *switchableKeystore) GetFirstDevice(ctx context.Context) (*waStore.Device, error) {
	if keystore := s.current(); keystore != nil {
		return keystore.GetFirstDevice(ctx)
	}
	return nil, errKeystoreUnavailable
}

func (s *switchableKeystore) GetDevice(ctx context.Context, jid types.JID) (*waStore.Device, error) {
	if keystore := s.current(); keystore != nil {
		return keystore.GetDevice(ctx, jid)
	}
	return nil, errKeystoreUnavailable
}

func (s *switchableKeystore) NewDevice() *waStore.Device {
	if keystore := s.current(); keystore != nil {
		return keystore.NewDevice()
	}
	return nil
}

func (s *switchableKeystore) PutDevice(ctx context.Context, device *waStore.Device) error {
	if keystore := s.current(); keystore != nil {
		return keystore.PutDevice(ctx, device)
	}
	return errKeystoreUnavailable
}

func (s *switchableKeystore) DeleteDevice(ctx context.Context, device *waStore.Device) error {
	if keystore := s.current(); keystore != nil {
		return keystore.DeleteDevice(ctx, device)
	}
	return errKeystoreUnavailable
}

type gatewayKeystoreRuntime struct {
	mu               sync.Mutex
	dsn              string
	holder           *switchableKeystore
	managed          *sqlitestore.Managed
	health           *gatewayv1.KeystoreHealth
	bootstrapAllowed bool
	reconciled       bool
	closed           bool
}

func openControlKeystore(ctx context.Context, dsn string) (*gatewayKeystoreRuntime, error) {
	runtime := &gatewayKeystoreRuntime{dsn: dsn, holder: &switchableKeystore{}}
	managed, err := wastore.OpenExisting(ctx, dsn, nil)
	if err == nil {
		runtime.install(managed)
		return runtime, nil
	}

	// Missing storage is recoverable only through a subsequent complete empty
	// snapshot. Corrupt and unclassified failures remain degraded and inert so
	// they cannot be mistaken for a new device store.
	switch {
	case errors.Is(err, sqlitestore.ErrKeystoreMissing):
		runtime.health = &gatewayv1.KeystoreHealth{State: gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_MISSING}
		runtime.bootstrapAllowed = true
	case errors.Is(err, sqlitestore.ErrKeystoreCorrupt):
		runtime.health = &gatewayv1.KeystoreHealth{State: gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_CORRUPT}
	default:
		return nil, err
	}
	return runtime, nil
}

func (r *gatewayKeystoreRuntime) install(managed *sqlitestore.Managed) {
	r.managed = managed
	r.holder.set(managed.Container)
	r.health = protobufKeystoreHealth(managed.Health())
	r.bootstrapAllowed = false
}

func (r *gatewayKeystoreRuntime) Health() *gatewayv1.KeystoreHealth {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.health == nil {
		return &gatewayv1.KeystoreHealth{State: gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_UNKNOWN}
	}
	result := &gatewayv1.KeystoreHealth{State: r.health.State}
	if r.health.ByteSize != nil {
		byteSize := *r.health.ByteSize
		result.ByteSize = &byteSize
	}
	if r.health.LastCheckedAtUnixMs != nil {
		checkedAt := *r.health.LastCheckedAtUnixMs
		result.LastCheckedAtUnixMs = &checkedAt
	}
	return result
}

// BootstrapEmpty creates storage only after the control stream has supplied a
// complete, authoritative empty assignment set. It is never a recovery path
// for corrupt storage or for a gateway with assigned sessions.
func (r *gatewayKeystoreRuntime) BootstrapEmpty(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.managed != nil || !r.bootstrapAllowed || r.closed {
		return nil
	}
	managed, err := wastore.OpenManaged(ctx, r.dsn, nil)
	if err != nil {
		return err
	}
	r.install(managed)
	return nil
}

func (r *gatewayKeystoreRuntime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.managed == nil {
		return nil
	}
	return r.managed.Close()
}

func (r *gatewayKeystoreRuntime) setReconciliation(report *gatewayv1.DesiredStateReport) {
	r.mu.Lock()
	r.reconciled = desiredStateReportHealthy(report)
	r.mu.Unlock()
}

func (r *gatewayKeystoreRuntime) RuntimeState() gatewayv1.GatewayRuntimeState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.health == nil || r.health.State != gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY || !r.reconciled {
		return gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_DEGRADED
	}
	return gatewayv1.GatewayRuntimeState_GATEWAY_RUNTIME_STATE_READY
}

func protobufKeystoreHealth(health sqlitestore.Health) *gatewayv1.KeystoreHealth {
	state := gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_UNKNOWN
	if health.Integrity == "ok" {
		state = gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY
	}
	result := &gatewayv1.KeystoreHealth{State: state}
	if health.Present {
		byteSize := health.ByteSize
		result.ByteSize = &byteSize
	}
	if !health.LastSuccessfulCheck.IsZero() {
		checkedAt := health.LastSuccessfulCheck.UnixMilli()
		result.LastCheckedAtUnixMs = &checkedAt
	}
	return result
}

// bootstrapControlApplier is the narrow composition-root policy seam around
// the transport-independent reconciler. A snapshot is authoritative only once
// it reaches the supervisor after protocol validation.
type bootstrapControlApplier struct {
	delegate *desiredstate.ControlApplier
	keystore *gatewayKeystoreRuntime
	onReport func(gatewayv1.GatewayRuntimeState)
}

func (a *bootstrapControlApplier) ApplyDesiredState(
	ctx context.Context,
	epoch uint64,
	snapshot *gatewayv1.DesiredStateSnapshot,
) (*gatewayv1.DesiredStateReport, error) {
	if len(snapshot.GetAssignments()) == 0 {
		if err := a.keystore.BootstrapEmpty(ctx); err != nil {
			return nil, err
		}
	}
	report, err := a.delegate.ApplyDesiredState(ctx, epoch, snapshot)
	if err == nil {
		a.keystore.setReconciliation(report)
		if a.onReport != nil {
			a.onReport(a.keystore.RuntimeState())
		}
	}
	return report, err
}

func desiredStateReportHealthy(report *gatewayv1.DesiredStateReport) bool {
	if report.GetKeystoreHealth().GetState() != gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY {
		return false
	}
	for _, result := range report.GetResults() {
		if result.GetStatus() != gatewayv1.ReconciliationResultStatus_RECONCILIATION_RESULT_STATUS_APPLIED {
			return false
		}
	}
	return true
}
