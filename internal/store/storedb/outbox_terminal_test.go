package storedb

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// SQLite executes the shared predicates; only MySQL's locking clause is
// removed. This catches terminal rows being selected, not merely SQL spelling.
func TestOutboxClaimsExcludeTerminalResults(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE outbox (id TEXT, organization_id TEXT, session_id TEXT,
 idempotency_key TEXT, payload BLOB, status TEXT, attempts INTEGER, next_attempt_at INTEGER,
 wa_message_id TEXT, error TEXT, terminal_at INTEGER, created_at INTEGER, updated_at INTEGER)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, status string
		terminal   any
	}{
		{"queued", "queued", nil}, {"stale", "sending", nil}, {"retry", "failed", nil},
		{"failed", "failed", 10}, {"sent", "sent", 10}, {"terminal-sending", "sending", 10},
	} {
		_, err = db.Exec(`INSERT INTO outbox VALUES (?, 'org', 'session', NULL, '{}', ?, 1, 0, NULL, NULL, ?, 0, 0)`, row.id, row.status, row.terminal)
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Query(strings.ReplaceAll(selectDueOutboxForClaim, "FOR UPDATE SKIP LOCKED", ""), "queued", "failed", 100, "sending", 50, 6)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for rows.Next() {
		values := make([]any, 13)
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
	if strings.Join(ids, ",") != "queued,retry,stale" {
		t.Fatalf("selected = %v", ids)
	}
	q := New(db)
	for _, id := range []string{"failed", "sent", "terminal-sending"} {
		n, err := q.ClaimOutboxByID(context.Background(), ClaimOutboxByIDParams{ClaimedStatus: "sending", UpdatedAt: 100, ID: id, QueuedStatus: "queued", FailedStatus: "failed", SendingStatus: "sending", StaleBefore: 50})
		if err != nil || n != 0 {
			t.Fatalf("terminal %s claimed: %d, %v", id, n, err)
		}
	}
}
