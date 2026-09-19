package application

import (
	"context"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// CommittedEventConsumer consumes an event only after the transaction that
// stores its durable envelope has committed. Implementations must use Event.ID
// as their idempotency key because a committed event can be replayed after an
// acknowledgement is lost.
//
// Authorization, gateway fencing, and durable event-log insertion are owned by
// the caller before this boundary. This port deliberately contains no transport
// details so journal replay, RPC delivery, and the transitional local path can
// all use the same post-commit consumers.
type CommittedEventConsumer interface {
	ConsumeCommittedEvent(context.Context, domain.Event) error
}

// CommittedEventClaim identifies one multi-replica-safe claim attempt. Owner is
// stable for a running worker. ClaimedAt is the eligibility cutoff; LeaseUntil
// is the new lease deadline. Both come from the same composition-owned clock.
type CommittedEventClaim struct {
	Owner      string
	ClaimedAt  time.Time
	LeaseUntil time.Time
	MaxItems   int
}

// CommittedEventWorkStore is the durable post-commit work boundary. Claim must
// return only event envelopes whose ingest transaction committed, and must make
// an unfinished claim available for retry after its lease expires or the worker
// crashes. Complete permanently records that every post-commit consumer has
// accepted the event ID, fenced by the owner that claimed it.
//
// The store implementation owns transaction, lease, and dedup details. Callers
// supply maxItems because batch sizing is a composition concern.
type CommittedEventWorkStore interface {
	ClaimCommittedEvents(context.Context, CommittedEventClaim) ([]domain.Event, error)
	CompleteCommittedEvent(context.Context, string, string) error
}
