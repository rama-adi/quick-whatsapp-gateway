package store

import (
	"context"
	"fmt"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
)

type outgoingEventIdentity struct {
	organizationID string
	sessionID      string
	chatJID        string
	waMessageID    string
}

// claimOutgoingEvent serializes API sends and gateway echoes on the WhatsApp
// message identity. The first transaction to commit owns the public event ID
// and its fan-out. Each caller still retains its own durable command/journal ID.
func claimOutgoingEvent(
	ctx context.Context,
	tx storedb.DBTX,
	identity outgoingEventIdentity,
	eventID, owner string,
) (claimedEventID, claimedOwner string, err error) {
	_, err = tx.ExecContext(ctx, `INSERT IGNORE INTO outgoing_message_event_claims
		(organization_id,session_id,chat_jid,wa_message_id,event_id,owner)
		VALUES (?,?,?,?,?,?)`, identity.organizationID, identity.sessionID,
		identity.chatJID, identity.waMessageID, eventID, owner)
	if err != nil {
		return "", "", fmt.Errorf("store: claim outgoing message event: %w", err)
	}
	err = tx.QueryRowContext(ctx, `SELECT event_id,owner FROM outgoing_message_event_claims
		WHERE organization_id=? AND session_id=? AND chat_jid=? AND wa_message_id=? FOR UPDATE`,
		identity.organizationID, identity.sessionID, identity.chatJID, identity.waMessageID,
	).Scan(&claimedEventID, &claimedOwner)
	if err != nil {
		return "", "", fmt.Errorf("store: read outgoing message event claim: %w", err)
	}
	return claimedEventID, claimedOwner, nil
}
