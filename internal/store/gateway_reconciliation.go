package store

import (
	"context"
	"database/sql"
	"fmt"
)

// GatewayReconciliationReport is a complete replacement of one gateway's
// current reconciliation view. Runtime status is intentionally absent.
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
// writes independent reconciliation/keystore health, then advances applied revision.
func (r *GatewayReconciliationRepo) Persist(ctx context.Context, report GatewayReconciliationReport, at int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var desired uint64
	if err = tx.QueryRowContext(ctx,
		`SELECT desired_revision FROM gateways WHERE id=? AND connection_epoch=? AND deleted_at IS NULL FOR UPDATE`,
		report.GatewayID, report.Epoch,
	).Scan(&desired); err != nil {
		return fmt.Errorf("reconciliation fence: %w", err)
	}
	if desired != report.Revision {
		return fmt.Errorf("reconciliation revision fence")
	}
	// Lock the full authoritative assignment set before accepting a report. A
	// healthy report may not omit an assigned session.
	rows, err := tx.QueryContext(ctx,
		`SELECT a.session_id, COALESCE(s.wa_jid,''), s.status IN ('starting','scan_qr_code','working') FROM gateway_session_assignments a JOIN wa_sessions s ON s.id=a.session_id WHERE a.gateway_id=? FOR UPDATE`,
		report.GatewayID,
	)
	if err != nil {
		return err
	}
	type assignment struct {
		device string
		run    bool
	}
	assigned, inventory := map[string]assignment{}, map[string]bool{}
	for rows.Next() {
		var id string
		var assignment assignment
		if err = rows.Scan(&id, &assignment.device, &assignment.run); err != nil {
			_ = rows.Close()
			return err
		}
		assigned[id] = assignment
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, device := range report.LocalDevices {
		if device == "" || inventory[device] {
			return fmt.Errorf("invalid local inventory")
		}
		inventory[device] = true
	}
	seen, classified := map[string]bool{}, map[string]bool{}
	degraded := report.KeystoreState != "healthy"
	for _, result := range report.Results {
		if result.Status == "unexpected_local_device" {
			if result.SessionID != nil || result.AssignmentEpoch != 0 || result.DeviceJID == "" || !inventory[result.DeviceJID] {
				return fmt.Errorf("unexpected device result has assignment")
			}
			classified[result.DeviceJID] = true
			degraded = true
			continue
		}
		if result.SessionID == nil || result.AssignmentEpoch == 0 || seen[*result.SessionID] {
			return fmt.Errorf("invalid assigned reconciliation result")
		}
		assignment, ok := assigned[*result.SessionID]
		if !ok || assignment.device != result.DeviceJID {
			return fmt.Errorf("assigned result device fence")
		}
		if result.Status == "applied" && assignment.run && (result.DeviceJID == "" || !inventory[result.DeviceJID]) {
			return fmt.Errorf("running applied device absent from inventory")
		}
		if result.Status == "keystore_missing" && result.DeviceJID != "" && inventory[result.DeviceJID] {
			return fmt.Errorf("missing device present in inventory")
		}
		if result.DeviceJID != "" {
			classified[result.DeviceJID] = true
		}
		seen[*result.SessionID] = true
		var n int
		if err = tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM gateway_session_assignments a JOIN wa_sessions s ON s.id=a.session_id WHERE a.gateway_id=? AND a.session_id=? AND a.assignment_epoch=? AND ((s.wa_jid IS NULL AND ?='') OR s.wa_jid=?)`,
			report.GatewayID, *result.SessionID, result.AssignmentEpoch, result.DeviceJID, result.DeviceJID,
		).Scan(&n); err != nil || n != 1 {
			return fmt.Errorf("assigned reconciliation result fence")
		}
		if result.Status != "applied" {
			degraded = true
		}
	}
	for id := range assigned {
		if !seen[id] {
			return fmt.Errorf("missing assignment result")
		}
	}
	for device := range inventory {
		if !classified[device] {
			return fmt.Errorf("unclassified inventory device")
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM gateway_reconciliation_results WHERE gateway_id=?`, report.GatewayID); err != nil {
		return err
	}
	for _, result := range report.Results {
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO gateway_reconciliation_results (gateway_id,device_jid,session_id,assignment_epoch,status,desired_revision,updated_at) VALUES (?,?,?,?,?,?,?)`,
			report.GatewayID,
			result.DeviceJID,
			result.SessionID,
			result.AssignmentEpoch,
			result.Status,
			report.Revision,
			at,
		); err != nil {
			return err
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
		return err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return fmt.Errorf("reconciliation update fence")
	}
	return tx.Commit()
}
