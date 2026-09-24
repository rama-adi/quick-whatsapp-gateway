package wa

import (
	"context"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// Session lifecycle bridges API-owned assignments and the local runtime.
//
// The API owns session rows, placement, and assignments; the gateway executes
// only the live parts. PrepareSession and ForgetSession validate the target
// alone: prepare is idempotent by construction and forget must still work when
// the row (and its assignment) is already deleted API-side. BeginPairing and
// PairPhone run behind the ownership fence but stay outside the ledger — a
// repeated QR read re-reads the live code, and each pairing code is a fresh
// one-time secret with nothing durable to replay. LogoutSession is destructive
// on WhatsApp's side, so it reuses the shared durable command executor.

// sessionController is the manager surface the lifecycle slices drive.
type sessionController interface {
	EnsureDevice(id, organizationID string)
	StartQR(ctx context.Context, id string) error
	StartPairingCode(ctx context.Context, id, phone string) (string, error)
	Logout(ctx context.Context, id string) error
	Forget(id string)
	LatestQR(id string) (code string, expiresAt int64)
}

// PrepareSession materializes the keystore device + managed-session entry for
// an API-created session row. Idempotent; not ledger-backed.
func (a *ApplicationGatewayAdapter) PrepareSession(
	_ context.Context,
	query application.SessionStateQuery,
) (application.PrepareSessionResult, error) {
	if err := a.validateTarget(query.OrganizationID, query.SessionID, query.GatewayID); err != nil {
		return application.PrepareSessionResult{}, err
	}
	a.controller.EnsureDevice(query.SessionID, query.OrganizationID)
	return application.PrepareSessionResult{
		MutationResult: mutationResult("", query.OrganizationID, query.SessionID, query.GatewayID, query.AssignmentEpoch),
	}, nil
}

// BeginPairing starts (or resumes) QR pairing and returns the current snapshot
// code when one exists. Read-classified: no command_id, no ledger.
func (a *ApplicationGatewayAdapter) BeginPairing(
	ctx context.Context,
	query application.SessionStateQuery,
) (application.PairingSnapshot, error) {
	if err := a.fencePairing(query); err != nil {
		return application.PairingSnapshot{}, err
	}
	if code, exp := a.controller.LatestQR(query.SessionID); code != "" {
		return application.PairingSnapshot{Code: code, ExpiresAt: exp}, nil
	}
	if err := a.controller.StartQR(ctx, query.SessionID); err != nil {
		return application.PairingSnapshot{}, err
	}
	code, exp := a.controller.LatestQR(query.SessionID)
	return application.PairingSnapshot{Code: code, ExpiresAt: exp}, nil
}

// PairPhone requests a phone-number pairing code. Not ledger-backed: every
// call yields a fresh one-time secret.
func (a *ApplicationGatewayAdapter) PairPhone(
	ctx context.Context,
	query application.SessionStateQuery,
	phone string,
) (string, error) {
	if err := a.fencePairing(query); err != nil {
		return "", err
	}
	if phone == "" {
		return "", domain.ErrValidation("phone is required")
	}
	return a.controller.StartPairingCode(ctx, query.SessionID, phone)
}

func (a *ApplicationGatewayAdapter) fencePairing(query application.SessionStateQuery) error {
	if err := a.validateTarget(query.OrganizationID, query.SessionID, query.GatewayID); err != nil {
		return err
	}
	if a.fence != nil && (query.AssignmentEpoch == 0 ||
		!a.fence.OwnsAssignment(query.OrganizationID, query.SessionID, query.AssignmentEpoch)) {
		return domain.ErrConflict("session assignment is not live")
	}
	return nil
}

// LogoutSession executes one durable logout command behind the assignment
// fence with full ledger semantics. Only CommandSent is recorded — logout has
// no WhatsApp message id — and stored failures replay as validation errors.
func (a *ApplicationGatewayAdapter) LogoutSession(
	ctx context.Context,
	command application.ContactJIDCommand,
) (application.MutationOnlyResult, error) {
	return runDurableCommand(ctx, a, durableCommand[application.MutationOnlyResult]{
		Operation:    "logout",
		AllowStopped: true,
		Target: mutationResult(
			command.CommandID,
			command.OrganizationID,
			command.SessionID,
			command.GatewayID,
			command.AssignmentEpoch,
		),
		Replay: func(record *application.CommandResultRecord) (application.MutationOnlyResult, error) {
			return replayedBlockingMutation(record)
		},
		Execute: func() (application.MutationOnlyResult, error) {
			err := a.controller.Logout(ctx, command.SessionID)
			if err != nil {
				return application.MutationOnlyResult{}, err
			}
			result := application.MutationOnlyResult{
				MutationResult: mutationResult(
					command.CommandID,
					command.OrganizationID,
					command.SessionID,
					command.GatewayID,
					command.AssignmentEpoch,
				),
			}
			if saveErr := a.saveCommand(ctx, application.CommandResultRecord{
				CommandID: command.CommandID, SessionID: command.SessionID,
				Status: application.CommandSent, UpdatedAt: a.now().UTC(),
			}); saveErr != nil {
				return application.MutationOnlyResult{}, saveErr
			}
			return result, nil
		},
	})
}

// ForgetSession drops a session's in-memory runtime during the delete flow.
// Idempotent; no epoch requirement because the row may already be deleted
// API-side — only the target is validated.
func (a *ApplicationGatewayAdapter) ForgetSession(_ context.Context, organizationID, sessionID string) error {
	if err := a.validateTarget(organizationID, sessionID, a.gatewayID); err != nil {
		return err
	}
	a.controller.Forget(sessionID)
	return nil
}

func sessionQueryFrom(
	organizationID string,
	sessionID string,
	gatewayID string,
	assignmentEpoch uint64,
) application.SessionStateQuery {
	return application.SessionStateQuery{
		OrganizationID:  organizationID,
		SessionID:       sessionID,
		GatewayID:       gatewayID,
		AssignmentEpoch: assignmentEpoch,
	}
}
