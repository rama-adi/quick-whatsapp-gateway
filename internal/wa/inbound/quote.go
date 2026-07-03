package inbound

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// enrichQuote fills the reply-context fields on a message event. The quoted
// author (quotedSenderJid/Lid) and quotedBody are already set from the reply's
// protocol frame at normalize time (guaranteed for genuine replies); this stage
// adds quotedFromMe — which the protocol frame does not carry reliably — by
// looking up the locally stored quoted message, and back-fills the author/body
// from that same row when the frame omitted them (best-effort). A quoted message
// older than the local retention window simply yields no local row, and the
// event keeps whatever the frame supplied.
func (p *Pipeline) enrichQuote(ctx context.Context, evt *domain.Event, nm *NormalizedMessage) error {
	if nm == nil || evt == nil {
		return nil
	}
	mp, ok := evt.Payload.(apitypes.MessagePayload)
	if !ok || mp.QuotedMessageID == "" {
		return nil
	}

	qc, found, err := p.repos.LookupQuotedContext(ctx, nm.SessionID, mp.QuotedMessageID)
	if err != nil {
		return fmt.Errorf("lookup quoted context: %w", err)
	}
	if !found {
		return nil // quote not in local storage; keep protocol-frame values as-is
	}

	mp.QuotedFromMe = qc.FromMe
	// Back-fill author/body only when the protocol frame did not already supply
	// them — the frame is the more authoritative source for a genuine reply.
	if mp.QuotedSenderJID == "" {
		mp.QuotedSenderJID = qc.SenderJID
	}
	if mp.QuotedSenderLID == "" {
		mp.QuotedSenderLID = qc.SenderLID
	}
	if mp.QuotedBody == "" {
		mp.QuotedBody = qc.Body
	}

	evt.Payload = mp
	// Keep the persisted raw_json in step with the enriched payload for the
	// message-bearing kinds (same contract as enrichMentions).
	if nm.Kind == KindMessage || nm.Kind == KindEdit {
		b, err := json.Marshal(evt.Payload)
		if err != nil {
			return fmt.Errorf("marshal quote-enriched payload: %w", err)
		}
		nm.RawJSON = b
	}
	return nil
}
