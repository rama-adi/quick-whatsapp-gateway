package store

import (
	"context"
	"errors"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// The outbox state machine on real MySQL: claims stamp a token and lease,
// every later write is fenced by the token, deferrals and projection retries
// are counted apart from dispatch attempts, and the CHECK constraints keep
// terminal_at and the lease consistent with the status.
func TestOutboxMySQLClaimFenceAndCounters(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	repo := NewOutboxRepo(db)
	org := "org_ob_" + domain.NewULID()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM outbox WHERE organization_id=?`, org) })
	insert := func(id string) {
		t.Helper()
		if err := repo.Insert(ctx, domain.OutboxEntry{ID: id, OrganizationID: org, SessionID: "ses",
			Payload: []byte(`{"type":"text"}`), Status: domain.OutboxQueued, CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	id := "ob_" + domain.NewULID()
	insert(id)
	claim := func(token string, now int64) []domain.OutboxEntry {
		t.Helper()
		got, err := repo.ClaimDue(ctx, OutboxClaim{Token: token, Now: now, LeaseUntil: now + 1000, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		var mine []domain.OutboxEntry
		for _, e := range got {
			if e.OrganizationID == org {
				mine = append(mine, e)
			}
		}
		return mine
	}
	first := domain.NewULID()
	if got := claim(first, 100); len(got) != 1 || got[0].ClaimToken != first || got[0].Status != domain.OutboxSending {
		t.Fatalf("claim = %+v", got)
	}
	if got := claim(domain.NewULID(), 500); len(got) != 0 {
		t.Fatal("live lease claimed twice")
	}
	// Rate-limit deferral: no dispatch attempt.
	if err := repo.Defer(ctx, id, first, "rate limited", 0, 200); err != nil {
		t.Fatal(err)
	}
	second := domain.NewULID()
	if got := claim(second, 300); len(got) != 1 || got[0].Deferrals != 1 || got[0].Attempts != 0 {
		t.Fatalf("after deferral = %+v", got)
	}
	if err := repo.MarkDispatch(ctx, id, first, 9000); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("stale token dispatched: %v", err)
	}
	if err := repo.MarkDispatch(ctx, id, second, 9000); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordAcknowledgement(ctx, id, second, "WA_1", 310); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeferProjection(ctx, id, second, "projection failed", 0, 320); err != nil {
		t.Fatal(err)
	}
	third := domain.NewULID()
	got := claim(third, 400)
	if len(got) != 1 || got[0].Attempts != 1 || got[0].ProjectionAttempts != 1 || got[0].AcknowledgedAt == nil {
		t.Fatalf("acknowledged reclaim = %+v", got)
	}
	// An acknowledged command can never be dispatched again.
	if err := repo.MarkDispatch(ctx, id, third, 9000); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("acknowledged command re-dispatched: %v", err)
	}
	if err := repo.CompleteOwned(ctx, id, second, domain.OutboxUnknown, nil, nil, 450); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("stale owner settled the command: %v", err)
	}
	waID := "WA_1"
	if err := repo.CompleteOwned(ctx, id, third, domain.OutboxSent, &waID, nil, 460); err != nil {
		t.Fatal(err)
	}
	final, err := repo.Get(ctx, id)
	if err != nil || final.Status != domain.OutboxSent || final.TerminalAt == nil || final.ClaimToken != "" {
		t.Fatalf("final = %+v err=%v", final, err)
	}
	// CHECK: a terminal status always carries terminal_at.
	if _, err := db.Exec(`INSERT INTO outbox (id,organization_id,session_id,payload,status,created_at,updated_at)
		VALUES (?,?, 'ses', '{}', 'failed', 1, 1)`, "ob_bad_"+domain.NewULID(), org); err == nil {
		t.Fatal("terminal row without terminal_at accepted")
	}
}
