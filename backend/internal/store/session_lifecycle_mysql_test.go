package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// Use the real schema: sqlmock cannot catch cascading foreign keys, or a lease
// predicate that accidentally admits rows owned by another worker.
func lifecycleTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("STORE_TEST_DSN")
	if dsn == "" || os.Getenv("STORE_TEST_DISPOSABLE") != "1" {
		t.Skip("requires STORE_TEST_DSN and STORE_TEST_DISPOSABLE=1")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var schema string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(schema, "qwg_store_test") {
		t.Fatalf("refusing disposable store test against %q", schema)
	}
	return db
}

func lifecycleFixture(t *testing.T, db *sql.DB) (string, domain.WASession) {
	t.Helper()
	id := domain.NewSessionID()
	gatewayID := "gw_" + id
	if _, err := db.Exec(
		`INSERT INTO gateways (id,status,connection_mode,connection_epoch,created_at,updated_at) VALUES (?,'active','control',1,1,1)`,
		gatewayID,
	); err != nil {
		t.Fatal(err)
	}
	session := domain.WASession{
		ID: id, OrganizationID: "test_org", GatewayID: gatewayID,
		Status: domain.SessionStopped, CreatedAt: 1, UpdatedAt: 1,
	}
	t.Cleanup(func() {
		for _, statement := range []string{
			`DELETE FROM gateway_ingested_events WHERE session_id=?`,
			`DELETE FROM event_log WHERE session_id=?`,
			`DELETE FROM wa_sessions WHERE id=?`,
		} {
			if _, err := db.Exec(statement, id); err != nil {
				t.Error(err)
			}
		}
		if _, err := db.Exec(`DELETE FROM gateways WHERE id=?`, gatewayID); err != nil {
			t.Error(err)
		}
	})
	return gatewayID, session
}

func TestSessionLifecycleMySQLAtomicCreationAndDeletion(t *testing.T) {
	db := lifecycleTestDB(t)
	gatewayID, session := lifecycleFixture(t, db)
	ctx := context.Background()
	repo := NewGatewayAssignmentRepo(db)

	// Placement can become ineligible after selection. Both the newly created
	// session and its assignment must roll back, so retry cannot leave an orphan.
	if _, err := db.Exec(`UPDATE gateways SET status='disabled' WHERE id=?`, gatewayID); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateSession(ctx, session); err == nil {
		t.Fatal("created session on disabled gateway")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM wa_sessions WHERE id=?`, session.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan sessions=%d err=%v", count, err)
	}
	if _, err := db.Exec(`UPDATE gateways SET status='active' WHERE id=?`, gatewayID); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	event := ingestEvent("evt_" + session.ID)
	event.GatewayID, event.SessionID, event.OrganizationID = gatewayID, session.ID, session.OrganizationID
	event.ConnectionEpoch, event.AssignmentEpoch = 1, 1
	if err := NewGatewayEventIngestRepo(db).Ingest(ctx, event, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM wa_sessions WHERE id=?`, session.ID); err == nil {
		t.Fatal("fixture did not exercise restrictive event FK")
	}
	if err := repo.DeleteSession(ctx, session.ID, 200); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"gateway_session_assignments", "gateway_ingested_events"} {
		if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE session_id=?", session.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("remaining %s=%d err=%v", table, count, err)
		}
	}
	var revision uint64
	if err := db.QueryRow(`SELECT desired_revision FROM gateways WHERE id=?`, gatewayID).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("revision=%d err=%v; want creation and deletion revisions", revision, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE event_id=?`, event.EventID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retained event logs=%d err=%v", count, err)
	}
}

func TestCommittedEventMySQLLeaseAndRetention(t *testing.T) {
	db := lifecycleTestDB(t)
	gatewayID, session := lifecycleFixture(t, db)
	ctx := context.Background()
	if err := NewGatewayAssignmentRepo(db).CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	repo := NewGatewayEventIngestRepo(db)
	event := ingestEvent("evt_" + session.ID)
	event.GatewayID, event.SessionID, event.OrganizationID = gatewayID, session.ID, session.OrganizationID
	event.ConnectionEpoch, event.AssignmentEpoch = 1, 1
	if err := repo.Ingest(ctx, event, 100); err != nil {
		t.Fatal(err)
	}
	claim := func(owner string, now int64) []domain.Event {
		t.Helper()
		events, err := repo.ClaimCommittedEvents(ctx, CommittedEventWork{
			Owner: owner, ClaimedAt: time.UnixMilli(now), LeaseUntil: time.UnixMilli(now + 1000), MaxItems: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return events
	}
	// A later event for the same session must not overtake work still leased
	// by another worker, even when the caller requests a smaller batch.
	later := event
	later.EventID += "_next"
	if err := repo.Ingest(ctx, later, 101); err != nil {
		t.Fatal(err)
	}
	if len(claim("first", 1000)) != 1 || len(claim("second", 1500)) != 0 {
		t.Fatal("a live lease was claimed twice")
	}
	if len(claim("second", 2000)) != 1 {
		t.Fatal("expired work was not reclaimed")
	}
	if err := repo.CompleteCommittedEvent(ctx, "first", event.EventID, time.UnixMilli(2100)); err == nil {
		t.Fatal("stale owner completed another worker's claim")
	}
	retention := NewRetentionRepo(db)
	if _, err := retention.Prune(ctx, 10_000); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE event_id=?`, event.EventID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retention erased unfinished work: count=%d err=%v", count, err)
	}
	if err := repo.CompleteCommittedEvent(ctx, "second", event.EventID, time.UnixMilli(2200)); err != nil {
		t.Fatal(err)
	}
	if _, err := retention.Prune(ctx, 10_000); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE event_id=?`, event.EventID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("completed event not pruned: count=%d err=%v", count, err)
	}
}

// Force the projection-before-intent interleaving directly: process E2E cannot
// schedule the MySQL projection commit relative to the desired-state write.
// TestSessionIntentMySQLNeverTouchesRuntime: user actions write only intent,
// each write advances the gateway revision, and only a new start bumps the
// start generation.
func TestSessionIntentMySQLNeverTouchesRuntime(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	gatewayID, session := lifecycleFixture(t, db)
	if err := NewGatewayAssignmentRepo(db).CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	state := NewSessionStateRepo(db)
	if applied, err := state.ApplyObservedStatus(ctx, ObservedStatus{
		SessionID: session.ID, Status: domain.SessionFailed, FailureReason: domain.FailureStreamReplaced, Seq: 5, ChangedAt: 50,
	}); err != nil || !applied {
		t.Fatalf("observed failure: applied=%v err=%v", applied, err)
	}
	generation, err := state.SetDesired(ctx, session.ID, domain.DesiredRun, true, 100)
	if err != nil || generation != 1 {
		t.Fatalf("start generation = %d, %v", generation, err)
	}
	if generation, err = state.SetDesired(ctx, session.ID, domain.DesiredRun, false, 110); err != nil || generation != 1 {
		t.Fatalf("repeated start generation = %d, %v", generation, err)
	}
	var desired, status string
	var reason sql.NullString
	if err := db.QueryRow(`SELECT s.desired_state, r.status, r.failure_reason FROM wa_sessions s JOIN wa_session_runtime r ON r.session_id=s.id WHERE s.id=?`, session.ID).
		Scan(&desired, &status, &reason); err != nil {
		t.Fatal(err)
	}
	if desired != "run" || status != "failed" || reason.String != "stream_replaced" {
		t.Fatalf("desired=%s status=%s reason=%v; intent must not touch the observed failure", desired, status, reason)
	}
	var revision uint64
	if err := db.QueryRow(`SELECT desired_revision FROM gateways WHERE id=?`, gatewayID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 3 {
		t.Fatalf("desired revision = %d; want creation plus two intent writes", revision)
	}
	// The snapshot reads intent only: the observed failure does not turn RUN
	// into STOP.
	assignments, current, err := NewGatewayRepo(db).DesiredStateForEpoch(ctx,
		domain.GatewayConnection{GatewayID: gatewayID, ConnectionEpoch: 1}, revision, 1000)
	if err != nil || !current || len(assignments) != 1 || !assignments[0].DesiredRun || assignments[0].StartGeneration != 1 {
		t.Fatalf("snapshot = %+v current=%v err=%v", assignments, current, err)
	}
}

// TestObservedStatusMySQLAppliesOnlyNewer: an older or repeated status never
// overwrites a newer one, and only a newer logged_out clears the identity.
func TestObservedStatusMySQLAppliesOnlyNewer(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	gatewayID, session := lifecycleFixture(t, db)
	jid := "62899" + session.ID[len(session.ID)-8:] + ":1@s.whatsapp.net"
	session.WAJID = &jid
	if err := NewGatewayAssignmentRepo(db).CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	state := NewSessionStateRepo(db)
	apply := func(status domain.SessionStatus, reason domain.SessionFailureReason, seq uint64) bool {
		t.Helper()
		applied, err := state.ApplyObservedStatus(ctx, ObservedStatus{
			SessionID: session.ID, Status: status, FailureReason: reason, Seq: seq, ChangedAt: int64(seq),
		})
		if err != nil {
			t.Fatal(err)
		}
		return applied
	}
	read := func() (string, sql.NullString, int64, sql.NullString) {
		t.Helper()
		var status string
		var reason, waJID sql.NullString
		var changed int64
		if err := db.QueryRow(`SELECT r.status, r.failure_reason, r.status_changed_at, s.wa_jid FROM wa_session_runtime r JOIN wa_sessions s ON s.id=r.session_id WHERE r.session_id=?`, session.ID).
			Scan(&status, &reason, &changed, &waJID); err != nil {
			t.Fatal(err)
		}
		return status, reason, changed, waJID
	}
	if !apply(domain.SessionWorking, "", 20) {
		t.Fatal("first observation not applied")
	}
	if apply(domain.SessionStarting, "", 10) || apply(domain.SessionStopped, "", 20) {
		t.Fatal("an older or equal sequence was applied")
	}
	if status, _, changed, _ := read(); status != "working" || changed != 20 {
		t.Fatalf("status = %s @ %d after stale observations", status, changed)
	}
	if apply(domain.SessionLoggedOut, "", 15) {
		t.Fatal("a stale logged_out applied")
	}
	if _, _, _, waJID := read(); !waJID.Valid {
		t.Fatal("a stale logged_out cleared the pairing identity")
	}
	if !apply(domain.SessionFailed, domain.FailureTemporaryBan, 30) {
		t.Fatal("newer failure not applied")
	}
	if status, reason, _, _ := read(); status != "failed" || reason.String != "temporary_ban" {
		t.Fatalf("failure = %s %v", status, reason)
	}
	var before uint64
	if err := db.QueryRow(`SELECT desired_revision FROM gateways WHERE id=?`, gatewayID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if !apply(domain.SessionLoggedOut, "", 40) {
		t.Fatal("newer logged_out not applied")
	}
	if status, reason, _, waJID := read(); status != "logged_out" || reason.Valid || waJID.Valid {
		t.Fatalf("logged out row = %s %v %v", status, reason, waJID)
	}
	var after uint64
	if err := db.QueryRow(`SELECT desired_revision FROM gateways WHERE id=?`, gatewayID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("identity clear did not advance the revision: %d -> %d", before, after)
	}
}

// TestRuntimeReportMySQLIsFenced: a report heals a lost status event, but only
// for the gateway's current connection and the session's current assignment.
func TestRuntimeReportMySQLIsFenced(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	gatewayID, session := lifecycleFixture(t, db)
	if err := NewGatewayAssignmentRepo(db).CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	state := NewSessionStateRepo(db)
	report := func(connectionEpoch, assignmentEpoch, seq uint64) int {
		t.Helper()
		applied, err := state.ApplyRuntimeReport(ctx, gatewayID, connectionEpoch, []RuntimeObservation{{
			AssignmentEpoch: assignmentEpoch,
			ObservedStatus:  ObservedStatus{SessionID: session.ID, Status: domain.SessionWorking, Seq: seq, ChangedAt: 9},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return applied
	}
	if report(2, 1, 50) != 0 || report(1, 7, 50) != 0 {
		t.Fatal("a report from a stale connection or assignment applied")
	}
	if report(1, 1, 0) != 0 {
		t.Fatal("a report without an observation applied")
	}
	if report(1, 1, 50) != 1 || report(1, 1, 50) != 0 {
		t.Fatal("report did not apply exactly once")
	}
}
