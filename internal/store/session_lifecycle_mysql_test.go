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
