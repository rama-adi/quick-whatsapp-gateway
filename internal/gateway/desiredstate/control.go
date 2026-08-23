package desiredstate

import (
	"context"
	"slices"
	"sync"
	"time"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
)

// ControlApplier translates the private protobuf into the transport-independent
// reconciler and produces the complete report which gates its acknowledgement.
type ControlApplier struct {
	Reconciler *Reconciler
	Health     func() *gatewayv1.KeystoreHealth
	// AfterFunc is injectable so expiry behavior is deterministic in tests.
	AfterFunc func(time.Duration, func()) Timer
	Now       func() time.Time
	// OnLeaseExpired immediately closes readiness after Expire stops sessions.
	OnLeaseExpired func(error)
	mu             sync.Mutex
	timer          Timer
}

type Timer interface{ Stop() bool }

func (a *ControlApplier) ApplyDesiredState(
	ctx context.Context,
	epoch uint64,
	snapshot *gatewayv1.DesiredStateSnapshot,
) (*gatewayv1.DesiredStateReport, error) {
	assignments := make([]Assignment, 0, len(snapshot.Assignments))
	for _, item := range snapshot.Assignments {
		config := item.GetConfig()
		assignment := Assignment{
			SessionID:       item.GetSessionId(),
			OrganizationID:  item.GetOrganizationId(),
			DeviceJID:       item.GetDeviceJid(),
			AssignmentEpoch: item.GetAssignmentEpoch(),
			LeaseExpiresAt:  time.UnixMilli(item.GetLeaseExpiresAtUnixMs()).UTC(),
			DesiredRun:      item.GetDesiredAction() == gatewayv1.SessionDesiredAction_SESSION_DESIRED_ACTION_RUN,
		}
		if config != nil {
			assignment.Config = Config{
				Revision:       config.GetRevision(),
				AutoRead:       config.GetAutoRead(),
				PresenceTyping: config.GetPresenceTyping(),
				RatePerMin:     config.GetRatePerMin(),
				RatePerHour:    config.GetRatePerHour(),
			}
		}
		assignments = append(assignments, assignment)
	}
	result, err := a.Reconciler.Apply(ctx, Snapshot{
		Revision:    snapshot.GetRevision(),
		Assignments: assignments,
	})
	if err != nil {
		return nil, err
	}
	a.resetLeaseTimer()
	report := a.newReport(epoch, snapshot)
	for _, device := range result.LocalDeviceJIDs {
		report.LocalDevices = append(report.LocalDevices, &gatewayv1.LocalDeviceInventory{DeviceJid: device})
	}
	report.Results = append(report.Results, assignmentResults(assignments, result)...)
	unexpectedDevice := gatewayv1.ReconciliationResultStatus_RECONCILIATION_RESULT_STATUS_UNEXPECTED_LOCAL_DEVICE
	corruptKeystore := gatewayv1.ReconciliationResultStatus_RECONCILIATION_RESULT_STATUS_KEYSTORE_CORRUPT
	report.Results = append(report.Results, jidResults(result.UnexpectedJIDs, unexpectedDevice)...)
	report.Results = append(report.Results, jidResults(result.CorruptJIDs, corruptKeystore)...)
	return report, nil
}

func (a *ControlApplier) newReport(
	epoch uint64,
	snapshot *gatewayv1.DesiredStateSnapshot,
) *gatewayv1.DesiredStateReport {
	report := &gatewayv1.DesiredStateReport{
		ConnectionEpoch:   epoch,
		ProcessedRevision: snapshot.GetRevision(),
		KeystoreHealth: &gatewayv1.KeystoreHealth{
			State: gatewayv1.KeystoreHealthState_KEYSTORE_HEALTH_STATE_HEALTHY,
		},
	}
	if a.Health != nil && a.Health() != nil {
		report.KeystoreHealth = a.Health()
	}
	return report
}

func assignmentResults(assignments []Assignment, result Result) []*gatewayv1.ReconciliationResult {
	results := make([]*gatewayv1.ReconciliationResult, 0, len(assignments))
	for _, assignment := range assignments {
		status := gatewayv1.ReconciliationResultStatus_RECONCILIATION_RESULT_STATUS_APPLIED
		if missingKeystore(result.MissingDeviceJIDs, assignment.DeviceJID) {
			status = gatewayv1.ReconciliationResultStatus_RECONCILIATION_RESULT_STATUS_KEYSTORE_MISSING
		}
		id := assignment.SessionID
		results = append(results, &gatewayv1.ReconciliationResult{
			SessionId:       &id,
			AssignmentEpoch: assignment.AssignmentEpoch,
			DeviceJid:       assignment.DeviceJID,
			Status:          status,
		})
	}
	return results
}

func missingKeystore(missing []string, deviceJID string) bool {
	return slices.Contains(missing, deviceJID)
}

func jidResults(jids []string, status gatewayv1.ReconciliationResultStatus) []*gatewayv1.ReconciliationResult {
	results := make([]*gatewayv1.ReconciliationResult, 0, len(jids))
	for _, jid := range jids {
		results = append(results, &gatewayv1.ReconciliationResult{DeviceJid: jid, Status: status})
	}
	return results
}

func (a *ControlApplier) resetLeaseTimer() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	expiresAt, ok := a.Reconciler.NextLeaseExpiry()
	if !ok {
		return
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	delay := expiresAt.Sub(now())
	if delay < 0 {
		delay = 0
	}
	after := a.AfterFunc
	if after == nil {
		after = func(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
	}
	a.timer = after(delay, func() {
		expired, err := a.Reconciler.Expire(context.Background())
		if expired && a.OnLeaseExpired != nil {
			a.OnLeaseExpired(err)
		}
	})
}

// Stop cancels the pending lease callback during terminal drain/shutdown.
func (a *ControlApplier) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
}
