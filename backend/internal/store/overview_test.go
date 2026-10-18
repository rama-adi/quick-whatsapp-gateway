package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// Rows arrive grouped by bucket index; Activity must fold them into
// zero-filled series of exactly len(Bounds), split by direction/status, and
// ignore out-of-range indexes defensively.
func TestOverviewActivityFoldsAndZeroFills(t *testing.T) {
	db, mock := newMock(t)
	repo := NewOverviewRepo(db)
	bounds := []int64{100, 200, 300, 400}

	mock.ExpectQuery(`INTERVAL\(timestamp, \?,\?,\?\).*FROM messages FORCE INDEX`).
		WithArgs(int64(200), int64(300), int64(400), "s1", "s2", int64(100), int64(450)).
		WillReturnRows(sqlmock.NewRows([]string{"bucket", "direction", "n"}).
			AddRow(0, "in", 2).AddRow(3, "in", 5).AddRow(3, "out", 1).AddRow(9, "in", 7))
	mock.ExpectQuery(`SELECT DISTINCT session_id, chat_jid`).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(4))
	mock.ExpectQuery(`FROM outbox`).
		WillReturnRows(sqlmock.NewRows([]string{"bucket", "n"}).AddRow(1, 3))
	mock.ExpectQuery(`SELECT id FROM webhooks`).WithArgs("org").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("wh1"))
	mock.ExpectQuery(`FROM webhook_deliveries FORCE INDEX \(idx_deliv_activity\)\s+WHERE webhook_id IN`).
		WillReturnRows(sqlmock.NewRows([]string{"bucket", "status", "n"}).
			AddRow(2, "delivered", 10).AddRow(2, "failed", 1).AddRow(2, "dead", 2))

	got, err := repo.Activity(context.Background(), OverviewWindow{
		OrganizationID: "org", SessionIDs: []string{"s1", "s2"}, Bounds: bounds, ToMs: 450,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := OverviewActivity{
		MessagesReceived:  []int64{2, 0, 0, 5},
		MessagesSent:      []int64{0, 0, 0, 1},
		SendsFailed:       []int64{0, 3, 0, 0},
		WebhookDeliveries: []int64{0, 0, 10, 0},
		WebhookFailures:   []int64{0, 0, 3, 0},
		ActiveChats:       4,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An organization without sessions or webhooks has nothing to count: only the
// id lookup runs, and every series is still fully zero-filled.
func TestOverviewActivityWithoutSessions(t *testing.T) {
	db, mock := newMock(t)
	mock.ExpectQuery(`SELECT id FROM webhooks`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	got, err := NewOverviewRepo(db).Activity(context.Background(), OverviewWindow{
		OrganizationID: "org", Bounds: []int64{1, 2, 3}, ToMs: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MessagesReceived) != 3 || len(got.WebhookFailures) != 3 || got.ActiveChats != 0 {
		t.Fatalf("got %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestOverviewMySQL runs the real queries against a migrated disposable schema:
// window edges, org isolation, the session filter (including the event_log
// join for deliveries), and has-sent detection.
func TestOverviewMySQL(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	suffix := domain.NewSessionID()
	orgA, orgB := "org_a_"+suffix, "org_b_"+suffix
	s1, s2, s3 := "s1_"+suffix, "s2_"+suffix, "s3_"+suffix
	hookA, hookB := "wh_a_"+suffix, "wh_b_"+suffix

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM messages WHERE session_id IN (?,?,?)`,
			`DELETE FROM outbox WHERE session_id IN (?,?,?)`,
			`DELETE FROM event_log WHERE session_id IN (?,?,?)`,
			`DELETE FROM wa_sessions WHERE id IN (?,?,?)`,
		} {
			_, _ = db.Exec(q, s1, s2, s3)
		}
		_, _ = db.Exec(`DELETE FROM webhook_deliveries WHERE webhook_id IN (?,?)`, hookA, hookB)
		_, _ = db.Exec(`DELETE FROM webhooks WHERE id IN (?,?)`, hookA, hookB)
	})

	for _, s := range []struct{ id, org string }{{s1, orgA}, {s2, orgA}, {s3, orgB}} {
		exec(`INSERT INTO wa_sessions (id, organization_id, gateway_id, created_at, updated_at) VALUES (?,?, 'gw_test', 1, 1)`, s.id, s.org)
	}
	msgN := 0
	msg := func(session, chat, dir string, ts int64) {
		msgN++
		exec(`INSERT INTO messages (id, session_id, wa_message_id, chat_jid, direction, type, timestamp, created_at)
			VALUES (?,?,?,?,?, 'text', ?, ?)`, fmt.Sprintf("msg_%s_%d", suffix, msgN), session, fmt.Sprintf("wa%d", msgN), chat, dir, ts, ts)
	}
	// Window: buckets [1000,2000), [2000,3000), [3000,3500) with to=3500.
	msg(s1, "c1", "in", 999)  // before window
	msg(s1, "c1", "in", 1000) // bucket 0 edge
	msg(s1, "c1", "in", 1999) // bucket 0
	msg(s1, "c2", "out", 2000)
	msg(s2, "c1", "in", 3499)  // last bucket, other session, same chat jid
	msg(s2, "c3", "out", 3500) // at `to`: excluded
	msg(s3, "c9", "in", 1500)  // other org

	outbox := func(id, org, session, status string, created int64) {
		exec(`INSERT INTO outbox (id, organization_id, session_id, payload, status, created_at, updated_at)
			VALUES (?,?,?, '{}', ?, ?, ?)`, id+suffix, org, session, status, created, created)
	}
	outbox("o1", orgA, s1, "failed", 1500)
	outbox("o2", orgA, s2, "failed", 3000)
	outbox("o3", orgA, s1, "unknown", 1500) // not a definite failure
	outbox("o4", orgB, s3, "failed", 1500)

	exec(`INSERT INTO webhooks (id, organization_id, url, events, retry_policy, created_at, updated_at)
		VALUES (?,?, 'https://a.example', '["*"]', '{}', 1, 1), (?,?, 'https://b.example', '["*"]', '{}', 1, 1)`,
		hookA, orgA, hookB, orgB)
	delivery := func(hook, session, status string, created int64) {
		eventID := "ev_" + domain.NewSessionID()
		exec(`INSERT INTO event_log (event_id, organization_id, session_id, type, payload, created_at)
			VALUES (?, 'x', ?, 'message.received', '{}', ?)`, eventID, session, created)
		exec(`INSERT INTO webhook_deliveries (webhook_id, event_id, status, created_at) VALUES (?,?,?,?)`,
			hook, eventID, status, created)
	}
	delivery(hookA, s1, "delivered", 1000)
	delivery(hookA, s2, "delivered", 2500)
	delivery(hookA, s1, "failed", 2500)
	delivery(hookA, s2, "dead", 3100)
	delivery(hookA, s1, "pending", 3100) // in flight: not counted
	delivery(hookB, s3, "delivered", 1000)

	repo := NewOverviewRepo(db)
	ids, err := repo.OrgSessionIDs(ctx, orgA)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("org sessions = %v", ids)
	}
	bounds := []int64{1000, 2000, 3000}

	all, err := repo.Activity(ctx, OverviewWindow{OrganizationID: orgA, SessionIDs: ids, Bounds: bounds, ToMs: 3500})
	if err != nil {
		t.Fatal(err)
	}
	wantAll := OverviewActivity{
		MessagesReceived:  []int64{2, 0, 1},
		MessagesSent:      []int64{0, 1, 0},
		SendsFailed:       []int64{1, 0, 1},
		WebhookDeliveries: []int64{1, 1, 0},
		WebhookFailures:   []int64{0, 1, 1},
		ActiveChats:       3, // (s1,c1) (s1,c2) (s2,c1)
	}
	if !reflect.DeepEqual(all, wantAll) {
		t.Fatalf("org-wide\n got %+v\nwant %+v", all, wantAll)
	}

	one, err := repo.Activity(ctx, OverviewWindow{OrganizationID: orgA, SessionID: s2, SessionIDs: []string{s2}, Bounds: bounds, ToMs: 3500})
	if err != nil {
		t.Fatal(err)
	}
	wantOne := OverviewActivity{
		MessagesReceived:  []int64{0, 0, 1},
		MessagesSent:      []int64{0, 0, 0},
		SendsFailed:       []int64{0, 0, 1},
		WebhookDeliveries: []int64{0, 1, 0},
		WebhookFailures:   []int64{0, 0, 1},
		ActiveChats:       1,
	}
	if !reflect.DeepEqual(one, wantOne) {
		t.Fatalf("session s2\n got %+v\nwant %+v", one, wantOne)
	}

	assertHasSent := func(org string, sessions []string, want bool) {
		t.Helper()
		got, err := repo.HasSentMessage(ctx, org, sessions)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("HasSentMessage(%s) = %v, want %v", org, got, want)
		}
	}
	assertHasSent(orgA, ids, true)           // outgoing message rows
	assertHasSent(orgB, []string{s3}, false) // only incoming, failed sends
	outbox("o5", orgB, s3, "sent", 10)       // an API send that reached WhatsApp
	assertHasSent(orgB, []string{s3}, true)
	exec(`DELETE FROM outbox WHERE id = ?`, "o5"+suffix)
	assertHasSent(orgB, nil, false)
}
