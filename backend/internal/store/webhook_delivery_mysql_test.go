package store

import (
	"context"
	"errors"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// A slow worker whose lease expired cannot double-mark a delivery or
// overwrite the outcome of the claim that replaced it; claims are ordered by
// (next_retry_at, id) and count each attempt when claimed.
func TestWebhookDeliveryMySQLTokenFencedMarks(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	repo := NewWebhookDeliveryRepo(db)
	hook := "wh_" + domain.NewULID()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM webhook_deliveries WHERE webhook_id=?`, hook) })
	// Same due time: the id tie-breaker decides the order.
	first, err := repo.Enqueue(ctx, hook, "evt_a_"+hook, 100)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.Enqueue(ctx, hook, "evt_b_"+hook, 100)
	if err != nil {
		t.Fatal(err)
	}
	claim := func(now int64) []domain.WebhookDelivery {
		t.Helper()
		got, err := repo.ClaimDue(ctx, now, 50)
		if err != nil {
			t.Fatal(err)
		}
		var mine []domain.WebhookDelivery
		for _, d := range got {
			if d.WebhookID == hook {
				mine = append(mine, d)
			}
		}
		return mine
	}
	slow := claim(1000)
	if len(slow) != 2 || slow[0].ID != first || slow[1].ID != second || slow[0].Status != domain.DeliveryDelivering ||
		slow[0].Attempts != 1 || slow[0].ClaimToken == "" {
		t.Fatalf("first claim = %+v", slow)
	}
	if again := claim(1000 + webhookAttemptLeaseMs/2); len(again) != 0 {
		t.Fatalf("live delivering rows reclaimed: %+v", again)
	}
	// The first row's lease expires; a new claim takes it with a new token.
	fresh := claim(1000 + webhookAttemptLeaseMs + 1)
	if len(fresh) != 1 || fresh[0].ID != first || fresh[0].Attempts != 2 || fresh[0].ClaimToken == slow[0].ClaimToken {
		t.Fatalf("reclaim = %+v", fresh)
	}
	if err := repo.MarkDelivered(ctx, fresh[0].ID, fresh[0].ClaimToken, 200); err != nil {
		t.Fatal(err)
	}
	// The slow worker finishes late: every outcome write is refused.
	if err := repo.MarkDelivered(ctx, slow[0].ID, slow[0].ClaimToken, 200); !errors.Is(err, ErrWebhookClaimLost) {
		t.Fatalf("late delivered: %v", err)
	}
	if err := repo.MarkFailed(ctx, slow[0].ID, slow[0].ClaimToken, nil, "timeout", 5000); !errors.Is(err, ErrWebhookClaimLost) {
		t.Fatalf("late failed: %v", err)
	}
	if err := repo.MarkDead(ctx, slow[0].ID, slow[0].ClaimToken, nil, "gave up"); !errors.Is(err, ErrWebhookClaimLost) {
		t.Fatalf("late dead: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM webhook_deliveries WHERE id=?`, first).Scan(&status); err != nil || status != "delivered" {
		t.Fatalf("delivered outcome overwritten: %s %v", status, err)
	}
	// The second row is still owned by the slow claim and can be completed.
	if err := repo.MarkFailed(ctx, slow[1].ID, slow[1].ClaimToken, nil, "503", 9000); err != nil {
		t.Fatal(err)
	}
}
