package store

import (
	"context"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// A running import whose owner stops heartbeating is interrupted by the
// reaper once its lease expires; a live lease is untouched; the owner fence
// keeps another process from finishing the row, while the real owner may
// still record a late outcome over 'interrupted'.
func TestBackfillImportMySQLLeaseReaper(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	repo := NewBackfillImportRepo(db)
	session := "sess_bfi_" + domain.NewULID()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM backfill_imports WHERE session_id=?`, session) })
	insert := func(id string, lease int64) domain.BackfillImport {
		t.Helper()
		job := domain.BackfillImport{ID: id, SessionID: session, OrganizationID: "org", Source: "crypt15",
			Status: "running", CreatedAt: 1000, Owner: "api-a", LeaseUntil: lease}
		if err := repo.Insert(ctx, job); err != nil {
			t.Fatal(err)
		}
		return job
	}
	crashed := insert("bfi_crashed_"+session, 2000)
	live := insert("bfi_live_"+session, 2000)
	if ok, err := repo.Heartbeat(ctx, live.ID, "api-a", 1500, 9000); err != nil || !ok {
		t.Fatalf("heartbeat ok=%v err=%v", ok, err)
	}
	if running, err := repo.HasLiveRunning(ctx, session, 2500); err != nil || !running {
		t.Fatalf("live import not seen: %v %v", running, err)
	}
	if n, err := repo.InterruptExpired(ctx, 2500); err != nil || n < 1 {
		t.Fatalf("interrupted=%d err=%v", n, err)
	}
	status := func(id string) string {
		var s string
		if err := db.QueryRow(`SELECT status FROM backfill_imports WHERE id=?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if status(crashed.ID) != "interrupted" || status(live.ID) != "running" {
		t.Fatalf("crashed=%s live=%s", status(crashed.ID), status(live.ID))
	}
	if ok, err := repo.Heartbeat(ctx, crashed.ID, "api-a", 2600, 9000); err != nil || ok {
		t.Fatalf("interrupted import renewed its lease: ok=%v err=%v", ok, err)
	}
	ids, err := repo.LiveIDs(ctx, 2500)
	if err != nil || len(ids) != 1 || ids[0] != live.ID {
		t.Fatalf("live ids=%v err=%v", ids, err)
	}
	done := live
	done.Status, done.Owner = "succeeded", "api-b"
	if err := repo.Finish(ctx, done); err == nil {
		t.Fatal("another owner finished the import")
	}
	late := crashed
	late.Status = "succeeded"
	if err := repo.Finish(ctx, late); err != nil || status(crashed.ID) != "succeeded" {
		t.Fatalf("late owner outcome not recorded: %v %s", err, status(crashed.ID))
	}
}
