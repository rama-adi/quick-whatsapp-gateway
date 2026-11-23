package storedb

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// SQLite executes the shared predicates; only MySQL's locking clause is
// removed. This catches terminal rows, live leases and undue rows being
// selected or leased, not merely SQL spelling.
func TestOutboxClaimsExcludeTerminalResults(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE outbox (id TEXT, organization_id TEXT, session_id TEXT,
 idempotency_key TEXT, payload BLOB, status TEXT, dispatch_attempts INTEGER, deferrals INTEGER,
 projection_attempts INTEGER, next_attempt_at INTEGER, wa_message_id TEXT, error TEXT, terminal_at INTEGER,
 created_at INTEGER, updated_at INTEGER, expires_at INTEGER, acknowledged_at INTEGER, effect_possible BOOLEAN,
 claim_token TEXT, lease_until INTEGER)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, status   string
		next         int64
		terminal     any
		token, lease any
	}{
		{"queued", "queued", 0, nil, nil, nil},
		{"acknowledged", "acknowledged", 0, nil, nil, nil},
		{"stale", "sending", 0, nil, "t", 50},
		{"live", "sending", 0, nil, "t", 500},
		{"later", "queued", 500, nil, nil, nil},
		{"failed", "failed", 0, 10, nil, nil},
		{"sent", "sent", 0, 10, nil, nil},
	} {
		_, err = db.Exec(`INSERT INTO outbox (id, organization_id, session_id, payload, status, dispatch_attempts,
 deferrals, projection_attempts, next_attempt_at, terminal_at, created_at, updated_at, effect_possible,
 claim_token, lease_until) VALUES (?, 'org', 'session', '{}', ?, 1, 0, 0, ?, ?, 0, 0, FALSE, ?, ?)`,
			row.id, row.status, row.next, row.terminal, row.token, row.lease)
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Query(strings.ReplaceAll(selectDueOutboxForClaim, "FOR UPDATE SKIP LOCKED", ""), 100, 100, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for rows.Next() {
		values := make([]any, 20)
		for i := range values {
			values[i] = new(any)
		}
		if err := rows.Scan(values...); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, (*values[0].(*any)).(string))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if strings.Join(ids, ",") != "acknowledged,queued,stale" { // ties broken by id
		t.Fatalf("selected = %v", ids)
	}
	q := New(db)
	for _, id := range []string{"failed", "sent", "live"} {
		n, err := q.LeaseOutbox(context.Background(), LeaseOutboxParams{
			ClaimToken: sql.NullString{String: "new", Valid: true}, LeaseUntil: sql.NullInt64{Int64: 900, Valid: true},
			Now: 100, ID: id, LeaseBefore: sql.NullInt64{Int64: 100, Valid: true},
		})
		if err != nil || n != 0 {
			t.Fatalf("%s leased: %d, %v", id, n, err)
		}
	}
}
