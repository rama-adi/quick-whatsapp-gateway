package gateway

import (
	"context"
	"errors"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// CommittedEventConsumer is the API transport adapter for a durable gateway
// event. Its caller must invoke it only after the ingest transaction commits;
// this preserves acknowledgement-loss replay without coupling the application
// dispatcher to a particular RPC or journal transport.
type CommittedEventConsumer struct {
	Dispatcher application.CommittedEventConsumer
}

func (c CommittedEventConsumer) ConsumeCommittedEvent(ctx context.Context, event domain.Event) error {
	if c.Dispatcher == nil {
		return errors.New("committed event dispatcher is not configured")
	}
	return c.Dispatcher.ConsumeCommittedEvent(ctx, event)
}

var _ application.CommittedEventConsumer = CommittedEventConsumer{}
