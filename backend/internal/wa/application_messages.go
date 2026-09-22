package wa

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/outbound"
)

// SendMessage executes one durable send command behind the assignment fence.
// A repeated CommandID returns the stored terminal result without re-dispatch;
// only definite outcomes (sent, or a pre-dispatch validation failure) are
// recorded. Transient and post-dispatch unknowns stay unrecorded so the API's
// retry re-issues the command and either replays or reconciles.
func (a *ApplicationGatewayAdapter) SendMessage(
	ctx context.Context,
	command application.SendCommand,
) (application.SendMessageResult, error) {
	return runDurableCommand(ctx, a, durableCommand[application.SendMessageResult]{
		Operation: "send",
		Target: mutationResult(
			command.CommandID,
			command.OrganizationID,
			command.SessionID,
			command.GatewayID,
			command.AssignmentEpoch,
		),
		Replay: func(record *application.CommandResultRecord) (application.SendMessageResult, error) {
			return replayedSendResult(command, record)
		},
		Execute: func() (application.SendMessageResult, error) {
			return a.executeSend(ctx, command)
		},
	})
}

func (a *ApplicationGatewayAdapter) executeSend(
	ctx context.Context,
	command application.SendCommand,
) (application.SendMessageResult, error) {
	waMessageID, ts, dispatchErr := a.dispatch.Dispatch(outbound.WithSessionID(ctx, command.SessionID), command.Payload)
	if dispatchErr != nil {
		// Only deterministic pre-dispatch rejections are terminal failures.
		// Everything else stays unrecorded for retry/reconciliation.
		var apiErr *domain.APIError
		if errors.As(dispatchErr, &apiErr) && apiErr.Code == domain.CodeValidationError {
			saveErr := a.saveCommand(ctx, application.CommandResultRecord{
				CommandID: command.CommandID, SessionID: command.SessionID,
				Status: application.CommandFailed, Error: dispatchErr.Error(), UpdatedAt: a.now().UTC(),
			})
			if saveErr != nil {
				return application.SendMessageResult{}, saveErr
			}
		}
		return application.SendMessageResult{}, dispatchErr
	}

	sentAt := a.now()
	if ts > 0 {
		sentAt = time.UnixMilli(ts).UTC()
	}
	result := application.SendMessageResult{
		MutationResult: mutationResult(
			command.CommandID,
			command.OrganizationID,
			command.SessionID,
			command.GatewayID,
			command.AssignmentEpoch,
		),
		WAMessageID: waMessageID,
		SentAt:      sentAt,
	}
	// Write-ahead response: the ledger row exists before this RPC answers, so
	// a lost response can be resolved by replay instead of re-dispatch.
	if saveErr := a.saveCommand(ctx, application.CommandResultRecord{
		CommandID: command.CommandID, SessionID: command.SessionID,
		Status: application.CommandSent, WAMessageID: waMessageID, UpdatedAt: sentAt,
	}); saveErr != nil {
		return application.SendMessageResult{}, saveErr
	}
	return result, nil
}

// replayedSendResult reconstructs the original outcome from one stored record.
// The stored UpdatedAt is the best available execution timestamp; the fence and
// epoch in the replayed result echo the current request's routing metadata.
func replayedSendResult(
	command application.SendCommand,
	record *application.CommandResultRecord,
) (application.SendMessageResult, error) {
	switch record.Status {
	case application.CommandSent:
		return application.SendMessageResult{
			MutationResult: mutationResult(
				record.CommandID,
				command.OrganizationID,
				command.SessionID,
				command.GatewayID,
				command.AssignmentEpoch,
			),
			WAMessageID: record.WAMessageID,
			SentAt:      record.UpdatedAt,
		}, nil
	case application.CommandFailed:
		return application.SendMessageResult{}, domain.ErrValidation("send previously failed: " + record.Error)
	default:
		return application.SendMessageResult{}, fmt.Errorf("unknown stored command status %q", record.Status)
	}
}

func validReceiptChatJID(jid types.JID) bool {
	if jid.User == "" {
		return false
	}
	switch jid.Server {
	case types.DefaultUserServer, types.LegacyUserServer, types.GroupServer, types.HiddenUserServer:
		return true
	default:
		return false
	}
}

func validReceiptSenderJID(jid types.JID) bool {
	if jid.User == "" {
		return false
	}
	switch jid.Server {
	case types.DefaultUserServer, types.LegacyUserServer, types.HiddenUserServer:
		return true
	default:
		return false
	}
}

// ExecuteOp runs one message sub-resource command behind the assignment fence
// with the same ledger semantics as SendMessage.
func (a *ApplicationGatewayAdapter) ExecuteOp(
	ctx context.Context,
	command application.MessageOpCommand,
) (application.MessageOpResult, error) {
	return runDurableCommand(ctx, a, durableCommand[application.MessageOpResult]{
		Operation: "message-op:" + string(command.Op),
		Target: mutationResult(
			command.CommandID,
			command.OrganizationID,
			command.SessionID,
			command.GatewayID,
			command.AssignmentEpoch,
		),
		Replay: func(record *application.CommandResultRecord) (application.MessageOpResult, error) {
			return replayedMutation(command, record)
		},
		Execute: func() (application.MessageOpResult, error) {
			return a.executeOp(ctx, command)
		},
	})
}

// executeOp runs only for the current command leader and commits definite
// outcomes before the shared executor releases waiting duplicates.
func (a *ApplicationGatewayAdapter) executeOp(
	ctx context.Context,
	command application.MessageOpCommand,
) (application.MessageOpResult, error) {
	waResult, opErr := a.opDispatch.DispatchOp(outbound.WithSessionID(ctx, command.SessionID), outbound.OpRequest{
		Op:      outbound.MessageOp(command.Op),
		Chat:    command.ChatJID,
		Sender:  command.SenderJID,
		MsgID:   command.MessageID,
		Emoji:   command.Emoji,
		NewText: command.NewText,
		Options: command.Options,
		To:      command.ToJID,
	})
	if opErr != nil {
		var apiErr *domain.APIError
		if errors.As(opErr, &apiErr) && apiErr.Code == domain.CodeValidationError {
			saveErr := a.saveCommand(ctx, application.CommandResultRecord{
				CommandID: command.CommandID, SessionID: command.SessionID,
				Status: application.CommandFailed, Error: opErr.Error(), UpdatedAt: a.now().UTC(),
			})
			if saveErr != nil {
				return application.MessageOpResult{}, saveErr
			}
		}
		return application.MessageOpResult{}, opErr
	}

	sentAt := a.now()
	if waResult.Timestamp > 0 {
		sentAt = time.UnixMilli(waResult.Timestamp).UTC()
	}
	out := application.MessageOpResult{
		MutationResult: mutationResult(
			command.CommandID,
			command.OrganizationID,
			command.SessionID,
			command.GatewayID,
			command.AssignmentEpoch,
		),
		WAMessageID: waResult.WAMessageID,
	}
	if saveErr := a.saveCommand(ctx, application.CommandResultRecord{
		CommandID: command.CommandID, SessionID: command.SessionID,
		Status: application.CommandSent, WAMessageID: waResult.WAMessageID, UpdatedAt: sentAt,
	}); saveErr != nil {
		return application.MessageOpResult{}, saveErr
	}
	return out, nil
}

// replayedMutation reconstructs a stored outcome for message-op commands. A
// stored failure replays as validation; a stored success returns routing
// metadata only (ops carry no additional response payload).
func replayedMutation(
	command application.MessageOpCommand,
	record *application.CommandResultRecord,
) (application.MessageOpResult, error) {
	switch record.Status {
	case application.CommandSent:
		return application.MessageOpResult{
			MutationResult: mutationResult(
				record.CommandID,
				command.OrganizationID,
				command.SessionID,
				command.GatewayID,
				command.AssignmentEpoch,
			),
			WAMessageID: record.WAMessageID,
		}, nil
	case application.CommandFailed:
		return application.MessageOpResult{}, domain.ErrValidation("op previously failed: " + record.Error)
	default:
		return application.MessageOpResult{}, fmt.Errorf("unknown stored command status %q", record.Status)
	}
}
