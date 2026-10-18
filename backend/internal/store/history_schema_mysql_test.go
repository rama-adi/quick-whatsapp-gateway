package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// TestAttachPairingMySQLTracksDataAccount pins relink detection: a new device
// of the same number is not an account change, a different number stamps
// account_changed_at, and logout keeps data_account_jid.
func TestAttachPairingMySQLTracksDataAccount(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	_, session := lifecycleFixture(t, db)
	if err := NewGatewayAssignmentRepo(db).CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	repo := NewSessionRepo(db)
	read := func() (sql.NullString, sql.NullInt64) {
		t.Helper()
		var account sql.NullString
		var changed sql.NullInt64
		if err := db.QueryRow(`SELECT data_account_jid, account_changed_at FROM wa_sessions WHERE id=?`, session.ID).
			Scan(&account, &changed); err != nil {
			t.Fatal(err)
		}
		return account, changed
	}
	attach := func(jid, phone string, at int64) {
		t.Helper()
		if err := repo.AttachPairing(ctx, AttachPairingInput{SessionID: session.ID, WaJID: jid, PhoneNumber: phone, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	// The JID is device-qualified and unique across sessions, so each pairing
	// below uses a distinct device suffix.
	phoneA := "62811" + session.ID[len(session.ID)-8:]
	attach(phoneA+":1@s.whatsapp.net", phoneA, 10)
	if account, changed := read(); account.String != phoneA+"@s.whatsapp.net" || changed.Valid {
		t.Fatalf("first pairing: account=%v changed=%v", account, changed)
	}
	if err := repo.ClearPairing(ctx, session.ID, 20); err != nil {
		t.Fatal(err)
	}
	attach(phoneA+":2@s.whatsapp.net", phoneA, 30)
	if account, changed := read(); account.String != phoneA+"@s.whatsapp.net" || changed.Valid {
		t.Fatalf("same number relink: account=%v changed=%v", account, changed)
	}
	if err := repo.ClearPairing(ctx, session.ID, 40); err != nil {
		t.Fatal(err)
	}
	if account, _ := read(); account.String != phoneA+"@s.whatsapp.net" {
		t.Fatalf("logout cleared data account: %v", account)
	}
	phoneB := "62822" + session.ID[len(session.ID)-8:]
	attach(phoneB+":1@s.whatsapp.net", phoneB, 50)
	if account, changed := read(); account.String != phoneB+"@s.whatsapp.net" || changed.Int64 != 50 {
		t.Fatalf("different number: account=%v changed=%v", account, changed)
	}
	if err := repo.ClearPairing(ctx, session.ID, 60); err != nil {
		t.Fatal(err)
	}
}

// TestMessageOriginMySQL stores rows from each source and reads origin back;
// a later live upsert of the same message keeps the original origin.
func TestMessageOriginMySQL(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	sessionID := domain.NewSessionID()
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM messages WHERE session_id=?`, sessionID); err != nil {
			t.Error(err)
		}
	})
	repo := NewMessageRepo(db)
	const chat = "120363000000000002@g.us"
	for id, origin := range map[string]domain.MessageOrigin{"A": "", "B": domain.OriginHistory, "C": domain.OriginBackup} {
		if err := repo.Upsert(ctx, domain.Message{
			SessionID: sessionID, WAMessageID: id, ChatJID: chat, Direction: domain.DirectionIn,
			Type: "text", Timestamp: 1, CreatedAt: 1, Origin: origin,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Upsert(ctx, domain.Message{
		SessionID: sessionID, WAMessageID: "B", ChatJID: chat, Direction: domain.DirectionIn,
		Type: "text", Timestamp: 1, CreatedAt: 2,
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.MessageOrigin{"A": domain.OriginLive, "B": domain.OriginHistory, "C": domain.OriginBackup}
	for id, origin := range want {
		got, err := repo.GetByWAID(ctx, sessionID, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Origin != origin {
			t.Fatalf("%s origin = %q, want %q", id, got.Origin, origin)
		}
	}
}
