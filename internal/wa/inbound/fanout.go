package inbound

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// fanout is stage 6 (§7.6): publish the envelope to live stream subscribers,
// enqueue webhook deliveries, and append to event_log (the durable cursor for
// stream resume).
//
// The three sinks are independent: a failure in one does not abort the others,
// so a flaky webhook queue never costs us the durable event_log row (which is
// what powers ?since= resume). Errors are joined and returned so the caller can
// decide whether to retry the whole event; persistence already happened, so a
// retry is idempotent at the message level (unique on session+wa_message_id).
func (p *Pipeline) fanout(ctx context.Context, evt domain.Event) error {
	var errs []error

	// Append to event_log first: it is the source of truth for resume, so we
	// prefer it to survive even if live delivery fails.
	if err := p.repos.AppendEventLog(ctx, evt); err != nil {
		errs = append(errs, err)
		p.log.ErrorContext(ctx, "fanout: append event_log failed",
			slog.String("event", evt.ID), slog.Any("err", err))
	}

	if err := p.sink.Publish(ctx, evt); err != nil {
		errs = append(errs, err)
		p.log.WarnContext(ctx, "fanout: publish failed",
			slog.String("event", evt.ID), slog.Any("err", err))
	}

	if err := p.webhooks.Enqueue(ctx, evt); err != nil {
		errs = append(errs, err)
		p.log.WarnContext(ctx, "fanout: webhook enqueue failed",
			slog.String("event", evt.ID), slog.Any("err", err))
	}

	return errors.Join(errs...)
}
