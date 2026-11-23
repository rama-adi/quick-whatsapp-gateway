package wa

import (
	"context"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/desiredstate"
)

// Session lifecycle bridges API-owned assignments and the local runtime.
//
// The API owns session rows, placement, and assignments; the gateway executes
// only the live parts. ForgetSession validates the target alone: it must still
// work when the row (and its assignment) is already deleted API-side.
// ApplySession, GetPairingQR and PairPhone run behind the ownership fence but
// stay outside the ledger — applying the same intent twice changes nothing, a
// QR read only reads, and each pairing code is a fresh one-time secret with
// nothing durable to replay. LogoutSession is destructive on WhatsApp's side,
// so it reuses the shared durable command executor.

// sessionController is the manager surface the lifecycle slices drive.
type sessionController interface {
	StartAssigned(ctx context.Context, assignment desiredstate.Assignment) error
	StopAssigned(ctx context.Context, sessionID string) error
	StartPairingCode(ctx context.Context, id, phone string) (string, error)
	SubmitPasskey(ctx context.Context, id string, response []byte) error
	ConfirmPasskey(ctx context.Context, id string) error
	Logout(ctx context.Context, id string) error
	Forget(id string)
	LatestQR(id string) (code string, expiresAt int64)
}

// ApplySession applies a session's desired state now: run starts it under
// the start-generation rules of Manager.StartAssigned (a newer generation
// starts a failed session and restarts a running one), stop ends its run. The
// next desired-state snapshot carries the same intent; this only saves the
// wait for it.
func (a *ApplicationGatewayAdapter) ApplySession(ctx context.Context, command application.ApplySessionCommand) error {
	query := command.SessionStateQuery
	if err := a.fencePairing(query); err != nil {
		return err
	}
	if !command.Run {
		return a.controller.StopAssigned(ctx, query.SessionID)
	}
	return a.controller.StartAssigned(ctx, desiredstate.Assignment{
		SessionID:       query.SessionID,
		OrganizationID:  query.OrganizationID,
		DeviceJID:       command.DeviceJID,
		AssignmentEpoch: query.AssignmentEpoch,
		DesiredRun:      true,
		StartGeneration: command.StartGeneration,
		// Config.Revision 0: keep the settings of the last snapshot.
	})
}

// GetPairingQR returns the current pairing QR code, if one exists. It never
// starts pairing: that is ApplySession's (and the snapshot's) job.
func (a *ApplicationGatewayAdapter) GetPairingQR(
	_ context.Context,
	query application.SessionStateQuery,
) (application.PairingSnapshot, error) {
	if err := a.fencePairing(query); err != nil {
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

// SubmitPasskey forwards the user's WebAuthn assertion to the pairing client.
func (a *ApplicationGatewayAdapter) SubmitPasskey(
	ctx context.Context,
	query application.SessionStateQuery,
	response []byte,
) error {
	if err := a.fencePairing(query); err != nil {
		return err
	}
	if len(response) == 0 {
		return domain.ErrValidation("passkey response is required")
	}
	return a.controller.SubmitPasskey(ctx, query.SessionID, response)
}

// ConfirmPasskey confirms the user saw the same code on the phone.
func (a *ApplicationGatewayAdapter) ConfirmPasskey(
	ctx context.Context,
	query application.SessionStateQuery,
) error {
	if err := a.fencePairing(query); err != nil {
		return err
	}
	return a.controller.ConfirmPasskey(ctx, query.SessionID)
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
