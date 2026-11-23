package store

import (
	"context"
	"database/sql"
	"fmt"
)

// GatewayReconciliationReport is a complete replacement of one gateway's
// current reconciliation view. Session runtime status travels in the same
// report but is stored separately (SessionStateRepo.ApplyRuntimeReport): it
// is ordered by status_seq, not by desired revision.
type GatewayReconciliationReport struct {
	GatewayID                string
	Epoch, Revision          uint64
	KeystoreState            string // healthy, missing, corrupt
	KeystoreBytes, CheckedAt *int64
	Results                  []GatewayReconciliationResult
	LocalDevices             []string
}
type GatewayReconciliationResult struct {
	SessionID         *string
	AssignmentEpoch   uint64
	DeviceJID, Status string
}
type GatewayReconciliationRepo struct{ db *sql.DB }

func NewGatewayReconciliationRepo(db *sql.DB) *GatewayReconciliationRepo {
	return &GatewayReconciliationRepo{db: db}
}

// Persist atomically validates the current assignment fence, replaces results,
// writes independent reconciliation/keystore health, then advances applied
// revision. A report for a revision that is no longer current is stale, not
// wrong: the desired state changed while the gateway applied the previous
// snapshot (for example a pairing attached its device). It is skipped with
// persisted=false; the snapshot of the new revision brings a fresh report.
func (r *GatewayReconciliationRepo) Persist(
	ctx context.Context,
	report GatewayReconciliationReport,
	at int64,
) (persisted bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var desired uint64
	if err = tx.QueryRowContext(ctx,
		`SELECT desired_revision FROM gateways WHERE id=? AND connection_epoch=? AND deleted_at IS NULL FOR UPDATE`,
		report.GatewayID, report.Epoch,
	).Scan(&desired); err != nil {
		return false, fmt.Errorf("reconciliation fence: %w", err)
	}
	if desired != report.Revision {
		return false, nil
	}
	// Lock the full authoritative assignment set before accepting a report. A
	// healthy report may not omit an assigned session. The revision is
	// current, so these intent columns are exactly what the gateway applied.
	rows, err := tx.QueryContext(ctx,
		`SELECT a.session_id, a.assignment_epoch, COALESCE(s.wa_jid,''), s.desired_state='run'
		FROM gateway_session_assignments a JOIN wa_sessions s ON s.id=a.session_id
		WHERE a.gateway_id=? FOR UPDATE`,
		report.GatewayID,
	)
	if err != nil {
		return false, err
	}
	type assignment struct {
		epoch  uint64
		device string
		run    bool
	}
	assigned, inventory := map[string]assignment{}, map[string]bool{}
	for rows.Next() {
		var id string
		var assignment assignment
		if err = rows.Scan(&id, &assignment.epoch, &assignment.device, &assignment.run); err != nil {
			_ = rows.Close()
			return false, err
		}
		assigned[id] = assignment
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	_ = rows.Close()
	for _, device := range report.LocalDevices {
		if device == "" || inventory[device] {
			return false, fmt.Errorf("invalid local inventory")
		}
		inventory[device] = true
	}
	seen, classified := map[string]bool{}, map[string]bool{}
	degraded := report.KeystoreState != "healthy"
	for _, result := range report.Results {
		if result.Status == "unexpected_local_device" {
			if result.SessionID != nil || result.AssignmentEpoch != 0 || result.DeviceJID == "" || !inventory[result.DeviceJID] {
				return false, fmt.Errorf("unexpected device result has assignment")
			}
			classified[result.DeviceJID] = true
			degraded = true
			continue
		}
		if result.SessionID == nil || result.AssignmentEpoch == 0 || seen[*result.SessionID] {
			return false, fmt.Errorf("invalid assigned reconciliation result")
		}
		assignment, ok := assigned[*result.SessionID]
		if !ok || assignment.epoch != result.AssignmentEpoch {
			return false, fmt.Errorf("assigned reconciliation result fence")
		}
		// A result names the device the gateway holds for the session. It
		// differs from the stored one only while a fresh pairing has not been
		// attached yet: the session holds its new device, the row has none.
		freshPairing := assignment.device == "" && result.DeviceJID != "" && inventory[result.DeviceJID]
		if assignment.device != result.DeviceJID && !freshPairing {
			return false, fmt.Errorf("assigned result device fence")
		}
		if result.Status == "applied" && assignment.run && assignment.device != "" && !inventory[result.DeviceJID] {
			return false, fmt.Errorf("running applied device absent from inventory")
		}
		if result.Status == "keystore_missing" && result.DeviceJID != "" && inventory[result.DeviceJID] {
			return false, fmt.Errorf("missing device present in inventory")
		}
		if result.DeviceJID != "" {
			classified[result.DeviceJID] = true
		}
		seen[*result.SessionID] = true
		if result.Status != "applied" {
			degraded = true
		}
	}
	for id := range assigned {
		if !seen[id] {
			return false, fmt.Errorf("missing assignment result")
		}
	}
	for device := range inventory {
		if !classified[device] {
			return false, fmt.Errorf("unclassified inventory device")
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM gateway_reconciliation_results WHERE gateway_id=?`, report.GatewayID); err != nil {
		return false, err
	}
	for _, result := range report.Results {
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO gateway_reconciliation_results (gateway_id,device_jid,session_id,assignment_epoch,status,desired_revision,updated_at) VALUES (?,?,?,?,?,?,?)`,
			report.GatewayID,
			nullStringFromValue(result.DeviceJID),
			result.SessionID,
			result.AssignmentEpoch,
			result.Status,
			report.Revision,
			at,
		); err != nil {
			return false, err
		}
	}
	status := "healthy"
	if degraded {
		status = "degraded"
	}
	present := report.KeystoreState != "missing"
	updated, err := tx.ExecContext(ctx,
		`UPDATE gateways SET reconciliation_status=?,keystore_present=?,keystore_bytes=?,keystore_integrity=?,keystore_checked_at=?,applied_revision=?,updated_at=? WHERE id=? AND connection_epoch=? AND desired_revision=?`,
		status,
		present,
		report.KeystoreBytes,
		report.KeystoreState,
		report.CheckedAt,
		report.Revision,
		at,
		report.GatewayID,
		report.Epoch,
		report.Revision,
	)
	if err != nil {
		return false, err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return false, fmt.Errorf("reconciliation update fence")
	}
	return true, tx.Commit()
}
