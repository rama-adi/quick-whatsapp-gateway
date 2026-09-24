package wa

import (
	"context"
	"fmt"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// durableCommand keeps resource-specific results typed while sharing the
// ordering that prevents duplicate side effects and publishes failures to waiters.
type durableCommand[T any] struct {
	Target       application.MutationResult
	Operation    string
	Replay       func(*application.CommandResultRecord) (T, error)
	Execute      func() (T, error)
	AllowStopped bool
}

type commandFlight struct {
	done      chan struct{}
	target    application.MutationResult
	operation string
	result    any
	err       error
}

func runDurableCommand[T any](
	ctx context.Context,
	adapter *ApplicationGatewayAdapter,
	command durableCommand[T],
) (result T, err error) {
	target := command.Target
	if err = adapter.validateMutation(
		target.CommandID,
		target.OrganizationID,
		target.SessionID,
		target.GatewayID,
	); err != nil {
		return result, err
	}
	if target.AssignmentEpoch == 0 {
		return result, domain.ErrValidation("assignment_epoch must be at least 1")
	}
	flight, follower, err := adapter.joinCommand(target, command.Operation)
	if err != nil {
		return result, err
	}
	if follower {
		select {
		case <-flight.done:
			value, ok := flight.result.(T)
			if !ok {
				return result, domain.ErrConflict("command id is already executing another operation")
			}
			return value, flight.err
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
	defer func() {
		flight.result, flight.err = result, err
		close(flight.done)
		adapter.inFlightMu.Lock()
		delete(adapter.inFlight, target.CommandID)
		adapter.inFlightMu.Unlock()
	}()

	// Lookup only after acquiring leadership: a completed previous flight may
	// have committed while this request was arriving. Replay precedes the fence
	// because the result already happened under a valid earlier assignment.
	record, err := adapter.lookupCommand(ctx, target.CommandID)
	if err != nil {
		return result, err
	}
	if record != nil {
		if record.SessionID != target.SessionID {
			return result, domain.ErrConflict("command id belongs to another session")
		}
		return command.Replay(record)
	}
	if adapter.fence != nil {
		allowed := adapter.fence.AllowsMutation(target.OrganizationID, target.SessionID, target.AssignmentEpoch)
		if command.AllowStopped {
			allowed = adapter.fence.OwnsAssignment(target.OrganizationID, target.SessionID, target.AssignmentEpoch)
		}
		if !allowed {
			return result, domain.ErrConflict("session assignment epoch is stale or lease expired")
		}
	}
	return command.Execute()
}

func (a *ApplicationGatewayAdapter) joinCommand(
	target application.MutationResult,
	operation string,
) (*commandFlight, bool, error) {
	a.inFlightMu.Lock()
	defer a.inFlightMu.Unlock()
	if flight, ok := a.inFlight[target.CommandID]; ok {
		sameOwner := flight.target.OrganizationID == target.OrganizationID &&
			flight.target.SessionID == target.SessionID && flight.target.GatewayID == target.GatewayID
		if !sameOwner || flight.operation != operation {
			return nil, false, domain.ErrConflict("command id is already executing for another target or operation")
		}
		// Epoch is intentionally excluded: a retry after reassignment can join an
		// already executing command rather than repeat its external side effect.
		return flight, true, nil
	}
	flight := &commandFlight{done: make(chan struct{}), target: target, operation: operation}
	a.inFlight[target.CommandID] = flight
	return flight, false, nil
}

func (a *ApplicationGatewayAdapter) lookupCommand(
	ctx context.Context,
	commandID string,
) (*application.CommandResultRecord, error) {
	if a.ledger == nil {
		return nil, domain.ErrValidation("gateway command ledger is not configured")
	}
	record, err := a.ledger.LookupCommand(ctx, commandID)
	if err != nil {
		return nil, fmt.Errorf("lookup command result: %w", err)
	}
	return record, nil
}

// Once a WhatsApp side effect finishes, caller cancellation must not suppress
// its local commit: retries need this record to avoid repeating the side effect.
func (a *ApplicationGatewayAdapter) saveCommand(ctx context.Context, record application.CommandResultRecord) error {
	if err := a.ledger.SaveCommandResult(context.WithoutCancel(ctx), record); err != nil {
		return fmt.Errorf("save command result %s: %w", record.CommandID, err)
	}
	return nil
}

func (a *ApplicationGatewayAdapter) validateMutation(commandID, organizationID, sessionID, gatewayID string) error {
	if commandID == "" {
		return domain.ErrValidation("command_id is required")
	}
	return a.validateTarget(organizationID, sessionID, gatewayID)
}

func (a *ApplicationGatewayAdapter) validateTarget(organizationID, sessionID, gatewayID string) error {
	missingTarget := organizationID == "" || sessionID == "" || gatewayID == ""
	if missingTarget {
		return domain.ErrValidation("organization_id, session_id, and gateway_id are required")
	}
	if gatewayID != a.gatewayID {
		return domain.ErrNotFound("gateway target does not match this gateway")
	}
	return nil
}

func mutationResult(
	commandID string,
	organizationID string,
	sessionID string,
	gatewayID string,
	assignmentEpoch uint64,
) application.MutationResult {
	return application.MutationResult{
		CommandID:       commandID,
		OrganizationID:  organizationID,
		SessionID:       sessionID,
		GatewayID:       gatewayID,
		AssignmentEpoch: assignmentEpoch,
	}
}
