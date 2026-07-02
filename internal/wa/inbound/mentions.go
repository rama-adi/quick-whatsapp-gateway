package inbound

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// enrichMentions projects the stored mention JID list into the public event
// shape. Persistence keeps messages.mentions as []string; only realtime/webhook
// message payloads expose resolved display metadata.
func (p *Pipeline) enrichMentions(ctx context.Context, evt *domain.Event, nm *NormalizedMessage) error {
	if nm == nil || len(nm.Mentions) == 0 || evt == nil {
		return nil
	}
	mp, ok := evt.Payload.(apitypes.MessagePayload)
	if !ok {
		return nil
	}

	mentions := make(map[string]apitypes.MentionData, len(nm.Mentions))
	if nm.IsGroup {
		details, err := p.repos.ResolveMentionDetails(ctx, nm.SessionID, nm.ChatJID, nm.Mentions)
		if err != nil {
			return fmt.Errorf("resolve mention details: %w", err)
		}
		for _, jid := range nm.Mentions {
			d := details[jid]
			mentions[jid] = apitypes.MentionData{
				PushName: d.PushName,
				Tag:      d.Tag,
			}
		}
	} else {
		for _, jid := range nm.Mentions {
			mentions[jid] = apitypes.MentionData{}
		}
	}
	if len(mentions) == 0 {
		return nil
	}

	mp.Mentions = mentions
	evt.Payload = mp
	if nm.Kind == KindMessage || nm.Kind == KindEdit {
		b, err := json.Marshal(evt.Payload)
		if err != nil {
			return fmt.Errorf("marshal mention-enriched payload: %w", err)
		}
		nm.RawJSON = b
	}
	return nil
}
