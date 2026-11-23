package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// fanoutFixture creates a session with an assignment and returns an ingest
// event template owned by it.
func fanoutFixture(t *testing.T, db *sql.DB) (GatewayEvent, domain.WASession) {
	t.Helper()
	gatewayID, session := lifecycleFixture(t, db)
	if err := NewGatewayAssignmentRepo(db).CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	event := ingestEvent("evt_" + session.ID)
	event.GatewayID, event.SessionID, event.OrganizationID = gatewayID, session.ID, session.OrganizationID
	event.ConnectionEpoch, event.AssignmentEpoch = 1, 1
	return event, session
}

func fanoutClaim(t *testing.T, repo *EventFanoutRepo, token string, now int64, perSession int) []ClaimedFanout {
	t.Helper()
	claimed, err := repo.Claim(context.Background(), FanoutClaim{
		Owner: "test", Token: token, ClaimedAt: time.UnixMilli(now), LeaseUntil: time.UnixMilli(now + 1000),
		MaxSessions: 10, MaxPerSession: perSession,
	})
	if err != nil {
		t.Fatal(err)
	}
	return claimed
}

func fanoutIDs(claimed []ClaimedFanout, session string) []string {
	var out []string
	for _, c := range claimed {
		if c.Event.Session == session {
			out = append(out, c.Event.ID)
		}
	}
	return out
}

func fanoutStatus(t *testing.T, db *sql.DB, eventID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM event_fanout WHERE event_id=?`, eventID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// One ingest batch shares committed_at; session_seq must still follow
// journal order, and the journal sequence is recorded as provenance.
func TestEventFanoutMySQLSessionSeqFollowsBatchOrder(t *testing.T) {
	db := lifecycleTestDB(t)
	template, session := fanoutFixture(t, db)
	ctx := context.Background()
	// IDs deliberately sort in reverse of journal order.
	var batch []GatewayEvent
	for i, id := range []string{"evt_z", "evt_m", "evt_a"} {
		e := template
		e.EventID = id + "_" + session.ID
		e.JournalSequence = uint64(10 + i)
		batch = append(batch, e)
	}
	if err := NewGatewayEventIngestRepo(db).IngestBatch(ctx, batch, 100); err != nil {
		t.Fatal(err)
	}
	claimed := fanoutClaim(t, NewEventFanoutRepo(db), domain.NewULID(), 1000, 10)
	got := fanoutIDs(claimed, session.ID)
	if len(got) != 3 || got[0] != batch[0].EventID || got[1] != batch[1].EventID || got[2] != batch[2].EventID {
		t.Fatalf("claim order %v, want journal order", got)
	}
	for i, c := range fanoutIDs(claimed, session.ID) {
		var seq, journal uint64
		if err := db.QueryRow(`SELECT f.session_seq, i.journal_sequence FROM event_fanout f
			JOIN gateway_ingested_events i ON i.event_log_id=f.event_id WHERE f.event_id=?`, c).Scan(&seq, &journal); err != nil {
			t.Fatal(err)
		}
		if seq != uint64(i+1) || journal != uint64(10+i) {
			t.Fatalf("%s: session_seq=%d journal_sequence=%d", c, seq, journal)
		}
	}
}

// Only a session's head is claimable, and only while no live lease holds it;
// a second replica cannot process the same session concurrently.
func TestEventFanoutMySQLHeadOfLineAndTokenFence(t *testing.T) {
	db := lifecycleTestDB(t)
	template, session := fanoutFixture(t, db)
	ctx := context.Background()
	ingest := NewGatewayEventIngestRepo(db)
	first, second := template, template
	first.EventID += "_1"
	second.EventID += "_2"
	for i, e := range []GatewayEvent{first, second} {
		if err := ingest.Ingest(ctx, e, int64(100+i)); err != nil {
			t.Fatal(err)
		}
	}
	repo := NewEventFanoutRepo(db)
	tokenA, tokenB := domain.NewULID(), domain.NewULID()
	if got := fanoutIDs(fanoutClaim(t, repo, tokenA, 1000, 1), session.ID); len(got) != 1 || got[0] != first.EventID {
		t.Fatalf("first claim = %v", got)
	}
	// The successor is not claimable while the head holds a live lease.
	if got := fanoutIDs(fanoutClaim(t, repo, tokenB, 1500, 10), session.ID); len(got) != 0 {
		t.Fatalf("live head lease was bypassed: %v", got)
	}
	// After the lease expires, another claim takes the head; the old token
	// can no longer start, complete or fail it.
	if got := fanoutIDs(fanoutClaim(t, repo, tokenB, 2500, 1), session.ID); len(got) != 1 || got[0] != first.EventID {
		t.Fatalf("expired head not reclaimed: %v", got)
	}
	if err := repo.Start(ctx, tokenA, first.EventID, time.UnixMilli(9000)); !errors.Is(err, ErrFanoutClaimLost) {
		t.Fatalf("stale start: %v", err)
	}
	if err := repo.Complete(ctx, tokenA, first.EventID, time.UnixMilli(2600)); !errors.Is(err, ErrFanoutClaimLost) {
		t.Fatalf("stale complete: %v", err)
	}
	if err := repo.Fail(ctx, FanoutFailure{Token: tokenA, EventID: first.EventID, Dead: true, At: time.UnixMilli(2600)}); !errors.Is(err, ErrFanoutClaimLost) {
		t.Fatalf("stale fail: %v", err)
	}
	if err := repo.Start(ctx, tokenB, first.EventID, time.UnixMilli(9000)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, tokenB, first.EventID, time.UnixMilli(2700)); err != nil {
		t.Fatal(err)
	}
	if got := fanoutIDs(fanoutClaim(t, repo, domain.NewULID(), 2800, 10), session.ID); len(got) != 1 || got[0] != second.EventID {
		t.Fatalf("successor after done = %v", got)
	}
}

// A retryable failure backs the head off and blocks its session; a
// dead-lettered head unblocks the session for the next event.
func TestEventFanoutMySQLDeadLetterUnblocksSession(t *testing.T) {
	db := lifecycleTestDB(t)
	template, session := fanoutFixture(t, db)
	ctx := context.Background()
	ingest := NewGatewayEventIngestRepo(db)
	poison, next := template, template
	poison.EventID += "_poison"
	next.EventID += "_next"
	if err := ingest.IngestBatch(ctx, []GatewayEvent{poison, next}, 100); err != nil {
		t.Fatal(err)
	}
	repo := NewEventFanoutRepo(db)
	token := domain.NewULID()
	if got := fanoutIDs(fanoutClaim(t, repo, token, 1000, 10), session.ID); len(got) != 2 {
		t.Fatalf("claim = %v", got)
	}
	if err := repo.Start(ctx, token, poison.EventID, time.UnixMilli(2000)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Fail(ctx, FanoutFailure{Token: token, EventID: poison.EventID, Error: "boom",
		NextAttemptAt: time.UnixMilli(5000), At: time.UnixMilli(1100)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Release(ctx, token, session.ID); err != nil {
		t.Fatal(err)
	}
	if fanoutStatus(t, db, next.EventID) != FanoutPending {
		t.Fatal("released successor is not pending")
	}
	if got := fanoutIDs(fanoutClaim(t, repo, domain.NewULID(), 2000, 10), session.ID); len(got) != 0 {
		t.Fatalf("backed-off head let its session advance: %v", got)
	}
	token = domain.NewULID()
	claimed := fanoutClaim(t, repo, token, 5000, 1)
	if got := fanoutIDs(claimed, session.ID); len(got) != 1 || got[0] != poison.EventID || claimed[0].Attempts != 1 || claimed[0].LastError != "boom" {
		t.Fatalf("retry claim = %+v", claimed)
	}
	if err := repo.Fail(ctx, FanoutFailure{Token: token, EventID: poison.EventID, Error: "undecodable", Dead: true,
		At: time.UnixMilli(5100)}); err != nil {
		t.Fatal(err)
	}
	if got := fanoutIDs(fanoutClaim(t, repo, domain.NewULID(), 5200, 10), session.ID); len(got) != 1 || got[0] != next.EventID {
		t.Fatalf("dead head did not unblock its session: %v", got)
	}
	backlog, err := repo.Backlog(ctx)
	if err != nil || backlog.Dead < 1 || backlog.Outstanding < 1 {
		t.Fatalf("backlog=%+v err=%v", backlog, err)
	}
}

// Unfinished fan-out keeps its event body; finished work releases it.
func TestEventFanoutMySQLRetentionKeepsUnfinishedEvents(t *testing.T) {
	db := lifecycleTestDB(t)
	event, session := fanoutFixture(t, db)
	ctx := context.Background()
	if err := NewGatewayEventIngestRepo(db).Ingest(ctx, event, 100); err != nil {
		t.Fatal(err)
	}
	retention := NewRetentionRepo(db)
	if _, err := retention.Prune(ctx, 10_000); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE event_id=?`, event.EventID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retention erased unfinished work: count=%d err=%v", count, err)
	}
	repo := NewEventFanoutRepo(db)
	token := domain.NewULID()
	if got := fanoutIDs(fanoutClaim(t, repo, token, 1000, 1), session.ID); len(got) != 1 {
		t.Fatal("event not claimable")
	}
	if err := repo.Complete(ctx, token, event.EventID, time.UnixMilli(1100)); err != nil {
		t.Fatal(err)
	}
	if _, err := retention.Prune(ctx, 10_000); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE event_id=?`, event.EventID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("completed event not pruned: count=%d err=%v", count, err)
	}
}
