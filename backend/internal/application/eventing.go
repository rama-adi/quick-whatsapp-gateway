package application

import (
	"context"
	"errors"
	"fmt"
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

// ErrPermanentEvent marks a consumer failure that no retry can fix (for
// example an undecodable payload). The committed-event worker dead-letters
// such an event at once instead of retrying it, which unblocks its session.
var ErrPermanentEvent = errors.New("permanent committed event failure")

// PermanentEventError wraps err so errors.Is(err, ErrPermanentEvent) holds.
func PermanentEventError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrPermanentEvent, err)
}

// Committed event sources. Gateway events are projected and then published;
// API events were projected by their producer and are only published.
const (
	CommittedEventSourceGateway = "gateway"
	CommittedEventSourceAPI     = "api"
)

// CommittedEventClaim identifies one multi-replica-safe claim attempt. Token
// is unique per claim and fences every later write for the claimed events;
// Owner names the replica for diagnostics. ClaimedAt is the eligibility
// cutoff; LeaseUntil is the claim's lease deadline.
type CommittedEventClaim struct {
	Owner         string
	Token         string
	ClaimedAt     time.Time
	LeaseUntil    time.Time
	MaxSessions   int
	MaxPerSession int
}

// ClaimedCommittedEvent is one leased event. Events of one session are
// returned contiguously in per-session sequence order.
type ClaimedCommittedEvent struct {
	Event      domain.Event
	Source     string
	SessionSeq uint64
	Attempts   int // attempts started before this claim
	LastError  string
	Missing    bool // the event body is gone; the event cannot be fanned out
}

// CommittedEventFailure records one failed attempt. Dead moves the event to
// the dead-letter state; otherwise it is retried at NextAttemptAt.
type CommittedEventFailure struct {
	Token, EventID, Error string
	Dead                  bool
	NextAttemptAt, At     time.Time
}

// CommittedEventBacklog summarizes outstanding and dead-lettered work.
type CommittedEventBacklog struct {
	Outstanding     int64
	Dead            int64
	OldestEventID   string
	OldestSessionID string
	OldestCreatedAt time.Time
	OldestAttempts  int
	OldestLastError string
}

// ErrCommittedEventClaimLost reports a fenced write that found the event
// claimed by a newer token.
var ErrCommittedEventClaimLost = errors.New("committed event claim lost")

// CommittedEventWorkStore is the durable post-commit work boundary. Every
// event has an explicit state (pending, leased, done, dead), a claim token,
// a lease, an attempt count and its last error. A session's events are
// processed strictly in per-session sequence order: only its oldest
// unfinished event is claimable, so a dead-lettered event unblocks the rest.
type CommittedEventWorkStore interface {
	ClaimCommittedEvents(context.Context, CommittedEventClaim) ([]ClaimedCommittedEvent, error)
	StartCommittedEvent(ctx context.Context, token, eventID string, leaseUntil time.Time) error
	CompleteCommittedEvent(ctx context.Context, token, eventID string, at time.Time) error
	FailCommittedEvent(context.Context, CommittedEventFailure) error
	ReleaseCommittedEvents(ctx context.Context, token, sessionID string) error
	CommittedEventBacklog(context.Context) (CommittedEventBacklog, error)
}
