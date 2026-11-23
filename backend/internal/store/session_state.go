package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
)

// SessionStateRepo owns the two halves of a session's lifecycle state and
// keeps each with exactly one kind of writer:
//
//   - Intent (wa_sessions.desired_state, start_generation) is written only by
//     user actions through SetDesired and ClearPairing.
//   - Observation (wa_session_runtime) is written only from the gateway, through
//     ApplyObservedStatus (committed session.status events) and
//     ApplyRuntimeReport (desired-state reports). Each write carries the
//     gateway's status_seq and applies only when it is newer than the stored
//     one, so a late or replayed status never overwrites a newer one.
//
// Every write that changes what the desired-state snapshot carries (desired
// state, start generation, pairing identity) advances the owning gateway's
// desired revision in the same transaction, so a revision always names one
// snapshot content.
type SessionStateRepo struct{ db *sql.DB }

// NewSessionStateRepo constructs a SessionStateRepo.
func NewSessionStateRepo(db *sql.DB) *SessionStateRepo { return &SessionStateRepo{db: db} }

// ObservedStatus is one gateway observation of a session's runtime status.
type ObservedStatus struct {
	SessionID     string
	Status        domain.SessionStatus
	FailureReason domain.SessionFailureReason // only with SessionFailed
	// Seq is the gateway's ordering key for this status; 0 never applies.
	Seq uint64
	// ChangedAt is when the gateway observed the change, epoch-ms.
	ChangedAt int64
}

func (o ObservedStatus) validate() error {
	if o.SessionID == "" || !o.Status.Observed() || o.ChangedAt <= 0 {
		return domain.ErrValidation("invalid observed session status")
	}
	if o.FailureReason != "" && (o.Status != domain.SessionFailed || !o.FailureReason.Valid()) {
		return domain.ErrValidation("invalid session failure reason")
	}
	return nil
}

// RuntimeObservation is one session's runtime as carried by a desired-state
// report, fenced by the assignment that produced it.
type RuntimeObservation struct {
	AssignmentEpoch uint64
	ObservedStatus
}

// SetDesired records what a user asked of a session. newStart bumps the start
// generation: the gateway then starts the session even when its last run
// ended terminally (failed, logged out), and restarts it if it is running. It
// returns the stored start generation.
func (r *SessionStateRepo) SetDesired(
	ctx context.Context,
	sessionID string,
	desired domain.DesiredState,
	newStart bool,
	at int64,
) (uint64, error) {
	if desired != domain.DesiredRun && desired != domain.DesiredStop {
		return 0, domain.ErrValidation("invalid desired session state")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin session desired update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockSessionAssignment(ctx, tx, sessionID); err != nil {
		return 0, err
	}
	bump := 0
	if newStart {
		bump = 1
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE wa_sessions SET desired_state=?, start_generation=start_generation+?, updated_at=? WHERE id=?`,
		string(desired), bump, at, sessionID,
	); err != nil {
		return 0, fmt.Errorf("store: update session desired state: %w", err)
	}
	var generation uint64
	if err := tx.QueryRowContext(ctx,
		`SELECT start_generation FROM wa_sessions WHERE id=?`, sessionID,
	).Scan(&generation); err != nil {
		return 0, fmt.Errorf("store: read session start generation: %w", err)
	}
	if err := advanceSessionGatewayRevision(ctx, tx, sessionID, at); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit session desired update: %w", err)
	}
	return generation, nil
}

// ClearPairing removes a session's WhatsApp identity after an explicit logout
// and records that the user wants it stopped. The runtime status follows from
// the gateway's logged_out observation.
func (r *SessionStateRepo) ClearPairing(ctx context.Context, sessionID string, at int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin session pairing clear: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockSessionAssignment(ctx, tx, sessionID); err != nil {
		return err
	}
	if err := clearIdentity(ctx, tx, sessionID, at); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE wa_sessions SET desired_state='stop' WHERE id=?`, sessionID,
	); err != nil {
		return fmt.Errorf("store: stop logged-out session: %w", err)
	}
	if err := advanceSessionGatewayRevision(ctx, tx, sessionID, at); err != nil {
		return err
	}
	return tx.Commit()
}

// AttachPairing persists the WhatsApp identity observed at PairSuccess: the
// device JID, its LID, and the phone number derived from the JID. The device
// JID is part of the desired-state snapshot, so the gateway revision advances
// with it. Replays rewrite the same identity. It also records
// data_account_jid (kept across logout) and stamps account_changed_at when the
// pairing attaches a different phone number than the one the session's stored
// data belongs to.
func (r *SessionStateRepo) AttachPairing(ctx context.Context, in AttachPairingInput) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin session pairing attach: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockSessionAssignment(ctx, tx, in.SessionID); err != nil {
		return err
	}
	// The account is the phone JID without the device suffix, so relinking
	// the same number as a new device does not count as an account change.
	var account sql.NullString
	if in.PhoneNumber != "" {
		account = sql.NullString{String: in.PhoneNumber + "@s.whatsapp.net", Valid: true}
	}
	n, err := storedb.New(tx).AttachSessionPairing(ctx, storedb.AttachSessionPairingParams{
		AccountJid:  account,
		ChangedAt:   sql.NullInt64{Int64: in.UpdatedAt, Valid: true},
		WaJid:       nullStringFromValue(in.WaJID),
		WaLid:       nullStringFromValue(in.WaLID),
		PhoneNumber: nullStringFromValue(in.PhoneNumber),
		UpdatedAt:   in.UpdatedAt,
		ID:          in.SessionID,
	})
	if err != nil {
		return fmt.Errorf("store: attach session pairing: %w", err)
	}
	if err := rowsAffectedOrNotFound(n, "session"); err != nil {
		return err
	}
	if err := advanceSessionGatewayRevision(ctx, tx, in.SessionID, in.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// ApplyObservedStatus stores one gateway observation if it is newer than the
// stored one. A newer logged_out also clears the pairing identity: the device
// is gone on the gateway, and the snapshot must stop naming it. applied is
// false for an older or equal observation.
func (r *SessionStateRepo) ApplyObservedStatus(ctx context.Context, o ObservedStatus) (bool, error) {
	if err := o.validate(); err != nil {
		return false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: begin observed status: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	applied, err := applyObservedStatus(ctx, tx, o)
	if err != nil || !applied {
		return false, err
	}
	return true, tx.Commit()
}

// ApplyRuntimeReport stores the runtime a gateway reported for its sessions,
// each fenced by the gateway's current connection epoch and the session's
// current assignment, and each applied only when newer. A status event the
// gateway dropped or the API never committed is therefore repaired by the next
// report (one heartbeat). It returns how many observations applied.
func (r *SessionStateRepo) ApplyRuntimeReport(
	ctx context.Context,
	gatewayID string,
	connectionEpoch uint64,
	observations []RuntimeObservation,
) (int, error) {
	if len(observations) == 0 {
		return 0, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin runtime report: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	applied := 0
	for _, o := range observations {
		if o.Seq == 0 {
			continue // the gateway has no observation of its own yet
		}
		if err := o.validate(); err != nil {
			return 0, err
		}
		var owned bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM gateway_session_assignments a JOIN gateways g ON g.id = a.gateway_id
			 WHERE a.session_id=? AND a.gateway_id=? AND a.assignment_epoch=? AND g.connection_epoch=?
			   AND g.deleted_at IS NULL)`,
			o.SessionID, gatewayID, o.AssignmentEpoch, connectionEpoch,
		).Scan(&owned); err != nil {
			return 0, fmt.Errorf("store: runtime report fence: %w", err)
		}
		if !owned {
			continue // reassigned or stale connection: not this gateway's to report
		}
		ok, err := applyObservedStatus(ctx, tx, o.ObservedStatus)
		if err != nil {
			return 0, err
		}
		if ok {
			applied++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit runtime report: %w", err)
	}
	return applied, nil
}

func applyObservedStatus(ctx context.Context, tx *sql.Tx, o ObservedStatus) (bool, error) {
	var stored uint64
	if err := tx.QueryRowContext(ctx,
		`SELECT status_seq FROM wa_session_runtime WHERE session_id=? FOR UPDATE`, o.SessionID,
	).Scan(&stored); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil // the session was deleted
		}
		return false, fmt.Errorf("store: lock session runtime: %w", err)
	}
	if o.Seq <= stored {
		return false, nil
	}
	var reason sql.NullString
	if o.FailureReason != "" {
		reason = sql.NullString{String: string(o.FailureReason), Valid: true}
	}
	// status_changed_at moves only when the status or reason changes (MySQL
	// evaluates SET left to right, so it compares against the stored values).
	if _, err := tx.ExecContext(ctx,
		`UPDATE wa_session_runtime
		 SET status_changed_at = IF(status <> ? OR NOT (failure_reason <=> ?), ?, status_changed_at),
		     status = ?, failure_reason = ?, status_seq = ?
		 WHERE session_id = ?`,
		string(o.Status), reason, o.ChangedAt, string(o.Status), reason, o.Seq, o.SessionID,
	); err != nil {
		return false, fmt.Errorf("store: apply observed session status: %w", err)
	}
	if o.Status == domain.SessionLoggedOut {
		if err := clearIdentity(ctx, tx, o.SessionID, o.ChangedAt); err != nil {
			return false, err
		}
		if err := advanceSessionGatewayRevision(ctx, tx, o.SessionID, o.ChangedAt); err != nil {
			return false, err
		}
	}
	return true, nil
}

// lockSessionAssignment locks the session's assignment row, the lock order
// every session-state and assignment transaction follows.
func lockSessionAssignment(ctx context.Context, tx *sql.Tx, sessionID string) error {
	var gatewayID string
	if err := tx.QueryRowContext(ctx,
		`SELECT gateway_id FROM gateway_session_assignments WHERE session_id=? FOR UPDATE`, sessionID,
	).Scan(&gatewayID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound("session assignment not found")
		}
		return fmt.Errorf("store: lock session assignment: %w", err)
	}
	return nil
}

func clearIdentity(ctx context.Context, tx *sql.Tx, sessionID string, at int64) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE wa_sessions SET wa_jid=NULL, wa_lid=NULL, phone_number=NULL, updated_at=? WHERE id=?`,
		at, sessionID,
	); err != nil {
		return fmt.Errorf("store: clear session pairing: %w", err)
	}
	return nil
}

// advanceSessionGatewayRevision bumps the desired revision of the gateway the
// session is assigned to.
func advanceSessionGatewayRevision(ctx context.Context, tx *sql.Tx, sessionID string, at int64) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE gateways g JOIN gateway_session_assignments a ON a.gateway_id = g.id
		 SET g.desired_revision = g.desired_revision + 1, g.updated_at = ?
		 WHERE a.session_id = ? AND g.deleted_at IS NULL`,
		at, sessionID,
	); err != nil {
		return fmt.Errorf("store: advance session gateway revision: %w", err)
	}
	return nil
}
