package service

import (
	"context"

	"go.mau.fi/whatsmeow"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// RoutingWAClient is the production outbound.WAClient. The outbound.Sender is
// account-global, but the live whatsmeow clients are per-session (owned by the
// wa.Manager). For each call the Sender stamps the target session id onto the
// context (outbound.WithSessionID); this client reads it back, resolves the live
// *whatsmeow.Client from the manager, wraps it with the per-session adapter
// (outbound.NewWhatsmeowClient) and delegates. When the session has no connected
// client, every method returns domain.ErrNotImplemented so a send fails loudly
// (mapped to the §11 not_implemented envelope) rather than panicking on nil.
type RoutingWAClient struct {
	resolve func(sessionID string) (*whatsmeow.Client, bool)
}

var _ outbound.WAClient = (*RoutingWAClient)(nil)

// clientResolver is the slice of *wa.Manager the router needs: resolve the live
// per-session whatsmeow client. *wa.Manager.ClientFor satisfies it.
type clientResolver interface {
	ClientFor(sessionID string) (*whatsmeow.Client, bool)
}

// NewRoutingWAClient builds a session-routing WAClient over the manager.
func NewRoutingWAClient(m clientResolver) *RoutingWAClient {
	return &RoutingWAClient{resolve: m.ClientFor}
}

// client resolves the per-session adapter for the session id carried on ctx.
func (c *RoutingWAClient) client(ctx context.Context) (outbound.WAClient, error) {
	id := outbound.SessionIDFromContext(ctx)
	if id == "" {
		return nil, domain.ErrNotImplemented("no target session for outbound send")
	}
	cli, ok := c.resolve(id)
	if !ok {
		return nil, domain.ErrNotImplemented("live WhatsApp client is not available for this session")
	}
	return outbound.NewWhatsmeowClient(cli), nil
}

func (c *RoutingWAClient) SendText(ctx context.Context, to, text, replyTo string, mentions []string) (string, int64, error) {
	w, err := c.client(ctx)
	if err != nil {
		return "", 0, err
	}
	return w.SendText(ctx, to, text, replyTo, mentions)
}

func (c *RoutingWAClient) SendPoll(ctx context.Context, to, name string, options []string, selectableCount int) (string, int64, error) {
	w, err := c.client(ctx)
	if err != nil {
		return "", 0, err
	}
	return w.SendPoll(ctx, to, name, options, selectableCount)
}

func (c *RoutingWAClient) SendLocation(ctx context.Context, to string, lat, lon float64, name string) (string, int64, error) {
	w, err := c.client(ctx)
	if err != nil {
		return "", 0, err
	}
	return w.SendLocation(ctx, to, lat, lon, name)
}

func (c *RoutingWAClient) SendContact(ctx context.Context, to, name, phone, vcard string) (string, int64, error) {
	w, err := c.client(ctx)
	if err != nil {
		return "", 0, err
	}
	return w.SendContact(ctx, to, name, phone, vcard)
}

func (c *RoutingWAClient) SendMedia(ctx context.Context, to, mediaType string, data []byte, mimetype, caption, filename, replyTo string, mentions []string) (string, int64, error) {
	w, err := c.client(ctx)
	if err != nil {
		return "", 0, err
	}
	return w.SendMedia(ctx, to, mediaType, data, mimetype, caption, filename, replyTo, mentions)
}

func (c *RoutingWAClient) React(ctx context.Context, chat, sender, msgID, emoji string) (string, int64, error) {
	w, err := c.client(ctx)
	if err != nil {
		return "", 0, err
	}
	return w.React(ctx, chat, sender, msgID, emoji)
}

func (c *RoutingWAClient) Edit(ctx context.Context, chat, msgID, newText string) (string, int64, error) {
	w, err := c.client(ctx)
	if err != nil {
		return "", 0, err
	}
	return w.Edit(ctx, chat, msgID, newText)
}

func (c *RoutingWAClient) Revoke(ctx context.Context, chat, sender, msgID string) (string, int64, error) {
	w, err := c.client(ctx)
	if err != nil {
		return "", 0, err
	}
	return w.Revoke(ctx, chat, sender, msgID)
}

func (c *RoutingWAClient) Vote(ctx context.Context, pollChat, pollSender, pollMsgID string, options []string) (string, int64, error) {
	w, err := c.client(ctx)
	if err != nil {
		return "", 0, err
	}
	return w.Vote(ctx, pollChat, pollSender, pollMsgID, options)
}

func (c *RoutingWAClient) Forward(ctx context.Context, to, sourceChat, sourceSender, sourceMsgID string) (string, int64, error) {
	w, err := c.client(ctx)
	if err != nil {
		return "", 0, err
	}
	return w.Forward(ctx, to, sourceChat, sourceSender, sourceMsgID)
}
