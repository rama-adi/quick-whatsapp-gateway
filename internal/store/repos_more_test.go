package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func gatewayRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "label", "status", "session_count", "capacity",
		"base_url", "last_seen_at", "created_at", "updated_at",
	})
}

func TestGatewayRepo_UpsertAndGet(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)

	g := domain.Gateway{
		ID: "gw_1", Label: strptr("primary"), Status: domain.GatewayActive,
		BaseURL: strptr("https://gw"), CreatedAt: 1, UpdatedAt: 2,
	}
	mock.ExpectExec("INSERT INTO gateways.*ON DUPLICATE KEY UPDATE").
		WithArgs(g.ID, g.Label, g.Status, g.SessionCount, g.Capacity, g.BaseURL, g.LastSeenAt, g.CreatedAt, g.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.Upsert(context.Background(), g); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	rows := gatewayRows().AddRow("gw_1", "primary", "active", 3, nil, "https://gw", nil, int64(1), int64(2))
	mock.ExpectQuery("SELECT .* FROM gateways WHERE id = .").
		WithArgs("gw_1").WillReturnRows(rows)
	got, err := repo.Get(context.Background(), "gw_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "gw_1" || got.Status != domain.GatewayActive || got.SessionCount != 3 {
		t.Fatalf("unexpected gateway: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRepo_Get_NotFound(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)
	mock.ExpectQuery("SELECT .* FROM gateways WHERE id = .").
		WithArgs("x").WillReturnError(noRows())
	_, err := repo.Get(context.Background(), "x")
	assertNotFound(t, err)
}

func TestGatewayRepo_HeartbeatAndSetStatus(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)

	mock.ExpectExec("UPDATE gateways SET last_seen_at=., session_count=., updated_at=. WHERE id=.").
		WithArgs(int64(100), 7, int64(100), "gw_1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.Heartbeat(context.Background(), "gw_1", 100, 7); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	mock.ExpectExec("UPDATE gateways SET status=., updated_at=. WHERE id=.").
		WithArgs(domain.GatewayDraining, int64(200), "gw_1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.SetStatus(context.Background(), "gw_1", domain.GatewayDraining, 200); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRepo_PickForPlacement(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)

	rows := gatewayRows().AddRow("gw_b", nil, "active", 1, nil, "https://b", int64(9), int64(1), int64(2))
	mock.ExpectQuery("SELECT .* FROM gateways").
		WithArgs(domain.GatewayActive).WillReturnRows(rows)
	got, err := repo.PickForPlacement(context.Background())
	if err != nil {
		t.Fatalf("PickForPlacement: %v", err)
	}
	if got.ID != "gw_b" {
		t.Fatalf("unexpected pick: %+v", got)
	}

	// No active gateway with headroom → not_found (router maps to 503).
	mock.ExpectQuery("SELECT .* FROM gateways").
		WithArgs(domain.GatewayActive).WillReturnError(noRows())
	if _, err := repo.PickForPlacement(context.Background()); err == nil {
		t.Fatal("expected not_found when no gateway available")
	} else {
		assertNotFound(t, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRepo_ListActive(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGatewayRepo(db)
	rows := gatewayRows().
		AddRow("gw_a", nil, "active", 0, nil, "https://a", int64(1), int64(1), int64(1)).
		AddRow("gw_b", nil, "active", 2, 10, "https://b", int64(1), int64(1), int64(1))
	mock.ExpectQuery("SELECT .* FROM gateways WHERE status = .").
		WithArgs(domain.GatewayActive).WillReturnRows(rows)
	got, err := repo.ListActive(context.Background())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(got) != 2 || got[0].ID != "gw_a" {
		t.Fatalf("unexpected list: %+v", got)
	}
}

func TestSessionRepo_CountByGateway(t *testing.T) {
	db, mock := newMock(t)
	repo := NewSessionRepo(db)
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM wa_sessions WHERE gateway_id = .").
		WithArgs("gw_1").WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(5))
	n, err := repo.CountByGateway(context.Background(), "gw_1")
	if err != nil || n != 5 {
		t.Fatalf("CountByGateway: n=%d err=%v", n, err)
	}
}

func TestWebhookRepo_CreateAndScanJSON(t *testing.T) {
	db, mock := newMock(t)
	repo := NewWebhookRepo(db)

	w := domain.Webhook{
		ID: "wh_1", OrganizationID: "ten_1", URL: "https://x", Events: []string{"message", "*"},
		HMACSecret: []byte{1, 2, 3}, CustomHeaders: map[string]string{"X": "Y"},
		RetryPolicy: domain.RetryPolicy{Policy: "exponential", DelaySeconds: 2, Attempts: 15},
		Active:      true, CreatedAt: 1, UpdatedAt: 1,
	}
	events, _ := json.Marshal(w.Events)
	headers, _ := json.Marshal(w.CustomHeaders)
	retry, _ := json.Marshal(w.RetryPolicy)
	mock.ExpectExec("INSERT INTO webhooks").
		WithArgs(w.ID, w.OrganizationID, w.SessionID, w.URL, events, w.HMACSecret, headers, retry, w.Active, w.CreatedAt, w.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatalf("Create: %v", err)
	}

	rows := sqlmock.NewRows([]string{
		"id", "organization_id", "session_id", "url", "events", "hmac_secret",
		"custom_headers", "retry_policy", "active", "created_at", "updated_at",
	}).AddRow("wh_1", "ten_1", nil, "https://x", events, []byte{1, 2, 3}, headers, retry, true, int64(1), int64(1))
	mock.ExpectQuery("SELECT .* FROM webhooks WHERE id = .").
		WithArgs("wh_1").WillReturnRows(rows)
	got, err := repo.Get(context.Background(), "wh_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Events) != 2 || got.Events[1] != "*" {
		t.Fatalf("events not scanned: %+v", got.Events)
	}
	if got.CustomHeaders["X"] != "Y" {
		t.Fatalf("headers not scanned: %+v", got.CustomHeaders)
	}
	if got.RetryPolicy.Attempts != 15 {
		t.Fatalf("retry policy not scanned: %+v", got.RetryPolicy)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookRepo_Create_NilHeaders(t *testing.T) {
	db, mock := newMock(t)
	repo := NewWebhookRepo(db)
	w := domain.Webhook{
		ID: "wh_2", OrganizationID: "ten_1", URL: "https://x", Events: []string{"*"},
		RetryPolicy: domain.RetryPolicy{Policy: "exponential"}, Active: true,
	}
	events, _ := json.Marshal(w.Events)
	retry, _ := json.Marshal(w.RetryPolicy)
	// nil custom headers must bind as SQL NULL.
	mock.ExpectExec("INSERT INTO webhooks").
		WithArgs(w.ID, w.OrganizationID, w.SessionID, w.URL, events, w.HMACSecret, nil, retry, w.Active, w.CreatedAt, w.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookDeliveryRepo_EnqueueAndClaim(t *testing.T) {
	db, mock := newMock(t)
	repo := NewWebhookDeliveryRepo(db)

	mock.ExpectExec("INSERT INTO webhook_deliveries").
		WithArgs("wh_1", "evt_1", domain.DeliveryPending, int64(100), int64(100)).
		WillReturnResult(sqlmock.NewResult(55, 1))
	id, err := repo.Enqueue(context.Background(), "wh_1", "evt_1", 100)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id != 55 {
		t.Fatalf("want id 55, got %d", id)
	}

	rows := sqlmock.NewRows([]string{
		"id", "webhook_id", "event_id", "status", "attempts", "response_code", "next_retry_at", "last_error", "created_at",
	}).AddRow(uint64(55), "wh_1", "evt_1", "failed", 2, nil, int64(90), "boom", int64(100))
	mock.ExpectQuery("SELECT .* FROM webhook_deliveries.*status IN ..,...*next_retry_at <= .*ORDER BY next_retry_at ASC").
		WithArgs(domain.DeliveryPending, domain.DeliveryFailed, int64(200), defaultLimit).
		WillReturnRows(rows)
	due, err := repo.ClaimDue(context.Background(), 200, 0)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(due) != 1 || due[0].ID != 55 || due[0].Status != domain.DeliveryFailed {
		t.Fatalf("unexpected due: %+v", due)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookDeliveryRepo_MarkFailedAndDead(t *testing.T) {
	db, mock := newMock(t)
	repo := NewWebhookDeliveryRepo(db)

	mock.ExpectExec("UPDATE webhook_deliveries.*status=., attempts=attempts.1").
		WithArgs(domain.DeliveryFailed, intptr(500), "err", int64(123), uint64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.MarkFailed(context.Background(), 9, intptr(500), "err", 123); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	mock.ExpectExec("UPDATE webhook_deliveries.*status=., attempts=attempts.1.*next_retry_at=NULL").
		WithArgs(domain.DeliveryDead, (*int)(nil), "dead", uint64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.MarkDead(context.Background(), 9, nil, "dead"); err != nil {
		t.Fatalf("MarkDead: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityRepo_UpsertAndGet(t *testing.T) {
	db, mock := newMock(t)
	repo := NewIdentityRepo(db)

	i := domain.Identity{LID: "111@lid", Name: strptr("Alice"), FirstSeenAt: 1, UpdatedAt: 2}
	mock.ExpectExec("INSERT INTO whatsapp_identities.*ON DUPLICATE KEY UPDATE.*COALESCE").
		WithArgs(i.LID, i.PhoneNumber, i.PhoneJID, i.Name, i.BusinessName, i.FirstSeenAt, i.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := repo.Upsert(context.Background(), i); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	rows := sqlmock.NewRows([]string{
		"id", "lid", "phone_number", "phone_jid", "name", "business_name", "first_seen_at", "updated_at",
	}).AddRow(uint64(1), "111@lid", "628", "628@s", "Alice", nil, int64(1), int64(2))
	mock.ExpectQuery("SELECT .* FROM whatsapp_identities WHERE lid = .").
		WithArgs("111@lid").WillReturnRows(rows)
	got, err := repo.GetByLID(context.Background(), "111@lid")
	if err != nil {
		t.Fatalf("GetByLID: %v", err)
	}
	if got.Name == nil || *got.Name != "Alice" {
		t.Fatalf("name not scanned: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityRepo_FillNameByJID(t *testing.T) {
	db, mock := newMock(t)
	repo := NewIdentityRepo(db)

	// Fills by lid-or-phone_jid match, only where name is currently empty.
	mock.ExpectExec("UPDATE whatsapp_identities SET name = ., updated_at = . WHERE .lid = . OR phone_jid = .. AND .name IS NULL OR name = ..").
		WithArgs("Alice", int64(7), "628@s.whatsapp.net", "628@s.whatsapp.net").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.FillNameByJID(context.Background(), "628@s.whatsapp.net", "Alice", 7); err != nil {
		t.Fatalf("FillNameByJID: %v", err)
	}

	// Empty jid or name is a no-op (no query issued).
	if err := repo.FillNameByJID(context.Background(), "", "Alice", 7); err != nil {
		t.Fatalf("FillNameByJID empty jid: %v", err)
	}
	if err := repo.FillNameByJID(context.Background(), "628@s.whatsapp.net", "", 7); err != nil {
		t.Fatalf("FillNameByJID empty name: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityRepo_NamesForMentions(t *testing.T) {
	db, mock := newMock(t)
	repo := NewIdentityRepo(db)

	// Two distinct mention JIDs; the lid match resolves, the phone_jid match too.
	rows := sqlmock.NewRows([]string{"lid", "phone_jid", "name"}).
		AddRow("205227043110953@lid", "628999@s.whatsapp.net", "Suci").
		AddRow("111@lid", nil, "Alice")
	mock.ExpectQuery(`SELECT lid, phone_jid, name FROM whatsapp_identities WHERE name IS NOT NULL AND name <> '' AND \(lid IN \(\?, \?\) OR phone_jid IN \(\?, \?\)\)`).
		WithArgs(
			"205227043110953@lid", "111@lid",
			"205227043110953@lid", "111@lid",
		).
		WillReturnRows(rows)

	got, err := repo.NamesForMentions(context.Background(), []string{
		"205227043110953@lid", "111@lid", "205227043110953@lid", // dup ignored
	})
	if err != nil {
		t.Fatalf("NamesForMentions: %v", err)
	}
	// Keyed by user-part (the "@<token>" form in a message body).
	if got["205227043110953"] != "Suci" || got["111"] != "Alice" {
		t.Fatalf("unexpected resolution: %+v", got)
	}

	// Empty input is a no-op (no query).
	if m, err := repo.NamesForMentions(context.Background(), nil); err != nil || m != nil {
		t.Fatalf("empty input: m=%v err=%v", m, err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGroupRepo_Upsert(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGroupRepo(db)
	g := domain.Group{GroupJID: "12@g.us", Subject: strptr("Lunch"), FirstSeenAt: 1, UpdatedAt: 2}
	mock.ExpectExec("INSERT INTO whatsapp_groups.*ON DUPLICATE KEY UPDATE.*COALESCE").
		WithArgs(g.GroupJID, g.Subject, g.Description, g.OwnerJID, g.ParticipantCount,
			g.IsAnnounce, g.IsLocked, g.CreatedAtWA, g.FirstSeenAt, g.UpdatedAt).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := repo.Upsert(context.Background(), g); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGroupRepo_ListBySession(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGroupRepo(db)
	cols := []string{
		"id", "group_jid", "subject", "description", "owner_jid",
		"participant_count", "is_announce", "is_locked", "created_at_wa", "first_seen_at", "updated_at",
	}
	rows := sqlmock.NewRows(cols).
		AddRow(uint64(1), "12@g.us", "Lunch", nil, nil, nil, nil, nil, nil, int64(1), int64(2)).
		AddRow(uint64(2), "34@g.us", "Work", nil, nil, nil, nil, nil, nil, int64(1), int64(2))
	mock.ExpectQuery("FROM whatsapp_groups g.*whatsapp_group_members WHERE session_id").
		WithArgs("sess_1").WillReturnRows(rows)
	got, err := repo.ListBySession(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(got) != 2 || got[0].GroupJID != "12@g.us" {
		t.Fatalf("unexpected groups: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGroupMemberRepo_UpsertAndListByContact(t *testing.T) {
	db, mock := newMock(t)
	repo := NewGroupMemberRepo(db)

	m := domain.GroupMember{
		SessionID: "sess_1", GroupJID: "12@g.us", LID: "111@lid",
		Tag: strptr("Al"), Role: domain.RoleAdmin, FirstSeenAt: 1, LastSeenAt: 2,
	}
	mock.ExpectExec("INSERT INTO whatsapp_group_members.*ON DUPLICATE KEY UPDATE").
		WithArgs(m.SessionID, m.GroupJID, m.LID, m.Tag, m.Role, m.FirstSeenAt, m.LastSeenAt).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := repo.Upsert(context.Background(), m); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	rows := sqlmock.NewRows([]string{
		"id", "session_id", "group_jid", "lid", "tag", "role", "first_seen_at", "last_seen_at",
	}).AddRow(uint64(1), "sess_1", "12@g.us", "111@lid", "Al", "admin", int64(1), int64(2))
	mock.ExpectQuery("SELECT .* FROM whatsapp_group_members WHERE session_id = . AND lid = .").
		WithArgs("sess_1", "111@lid").WillReturnRows(rows)
	got, err := repo.ListByContact(context.Background(), "sess_1", "111@lid")
	if err != nil {
		t.Fatalf("ListByContact: %v", err)
	}
	if len(got) != 1 || got[0].Role != domain.RoleAdmin {
		t.Fatalf("unexpected members: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChatRepo_UpsertAndUpdateFlags(t *testing.T) {
	db, mock := newMock(t)
	repo := NewChatRepo(db)

	c := domain.Chat{SessionID: "sess_1", ChatJID: "628@s", Type: domain.ChatDM, Name: strptr("Al"), LastMessageAt: i64ptr(50)}
	mock.ExpectExec("INSERT INTO chats.*ON DUPLICATE KEY UPDATE.*GREATEST").
		WithArgs(c.SessionID, c.ChatJID, c.Type, c.Name, c.LastMessageAt, c.UnreadCount, c.Archived, c.Pinned, c.MutedUntil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := repo.Upsert(context.Background(), c); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	mock.ExpectExec("UPDATE chats SET archived=., pinned=., muted_until=., unread_count=. WHERE session_id=. AND chat_jid=.").
		WithArgs(true, false, i64ptr(999), 0, "sess_1", "628@s").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.UpdateFlags(context.Background(), "sess_1", "628@s", true, false, i64ptr(999), 0); err != nil {
		t.Fatalf("UpdateFlags: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPollVoteRepo_InsertAndList(t *testing.T) {
	db, mock := newMock(t)
	repo := NewPollVoteRepo(db)

	v := domain.PollVote{
		SessionID: "sess_1", PollMessageID: "poll_1", VoterLID: "111@lid",
		SelectedOptions: json.RawMessage(`["Pizza"]`), Timestamp: 10,
	}
	mock.ExpectExec("INSERT INTO poll_votes").
		WithArgs(v.SessionID, v.PollMessageID, v.VoterLID, []byte(v.SelectedOptions), v.Timestamp, nil).
		WillReturnResult(sqlmock.NewResult(3, 1))
	id, err := repo.Insert(context.Background(), v)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id != 3 {
		t.Fatalf("want id 3, got %d", id)
	}

	rows := sqlmock.NewRows([]string{"id", "session_id", "poll_message_id", "voter_lid", "selected_options", "timestamp", "raw_json"}).
		AddRow(uint64(3), "sess_1", "poll_1", "111@lid", []byte(`["Pizza"]`), int64(10), nil)
	mock.ExpectQuery("SELECT .* FROM poll_votes WHERE session_id = . AND poll_message_id = . ORDER BY id ASC").
		WithArgs("sess_1", "poll_1").WillReturnRows(rows)
	got, err := repo.ListByPoll(context.Background(), "sess_1", "poll_1")
	if err != nil {
		t.Fatalf("ListByPoll: %v", err)
	}
	if len(got) != 1 || string(got[0].SelectedOptions) != `["Pizza"]` {
		t.Fatalf("selected_options not scanned: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
