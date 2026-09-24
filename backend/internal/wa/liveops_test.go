package wa

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func lid(user string, device uint16) types.JID {
	return types.JID{User: user, Server: types.HiddenUserServer, Device: device}
}

func pn(user string) types.JID {
	return types.JID{User: user, Server: types.DefaultUserServer}
}

func TestContactName_Precedence(t *testing.T) {
	if got := contactName(types.ContactInfo{PushName: "Push", FullName: "Full", FirstName: "First"}); got != "Push" {
		t.Fatalf("want push name preferred, got %q", got)
	}
	if got := contactName(types.ContactInfo{FullName: "Full", FirstName: "First"}); got != "Full" {
		t.Fatalf("want full name fallback, got %q", got)
	}
	if got := contactName(types.ContactInfo{FirstName: "First"}); got != "First" {
		t.Fatalf("want first name fallback, got %q", got)
	}
	if got := contactName(types.ContactInfo{}); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

// TestBackfillMember_CanonicalizesAndRoles backfills a device-qualified group participant with
// contact metadata and admin flags. The result must use canonical IDs, resolved names, and the correct
// owner/admin/member role without mutating unrelated fields.
func TestBackfillMember_CanonicalizesAndRoles(t *testing.T) {
	// Push name index resolves the member's display name by canonical LID, even
	// though the participant struct itself only carries an obfuscated DisplayName.
	names := map[string]string{"199127753306132@lid": "Agung rahma"}
	m, ok := backfillMember(context.Background(), nil, names, types.GroupParticipant{
		LID:         lid("199127753306132", 9),
		PhoneNumber: pn("6282147077374"),
		IsAdmin:     true,
		DisplayName: "+62?????????22", // obfuscated → not a nickname
	})
	if !ok {
		t.Fatal("expected ok for a participant with a LID")
	}
	if m.LID != "199127753306132@lid" {
		t.Fatalf("LID not canonicalized: %q", m.LID)
	}
	if m.Name != "Agung rahma" {
		t.Fatalf("push name not resolved from index: %q", m.Name)
	}
	if m.PhoneNumber != "6282147077374" || m.JID != "6282147077374@s.whatsapp.net" {
		t.Fatalf("phone not captured: %+v", m)
	}
	// The per-group tag is the raw WhatsApp DisplayName (kept as-is on the pivot).
	if m.Tag != "+62?????????22" {
		t.Fatalf("tag should be the raw display name, got %q", m.Tag)
	}
	if m.Role != domain.RoleAdmin {
		t.Fatalf("role = %q, want admin", m.Role)
	}

	// No name in the index → empty (filled later by message capture), not crash.
	m2, ok := backfillMember(context.Background(), nil, nil, types.GroupParticipant{
		LID: lid("196086799012038", 0),
	})
	if !ok || m2.Name != "" {
		t.Fatalf("expected ok with empty name, got ok=%v name=%q", ok, m2.Name)
	}

	// A participant with neither LID nor a resolvable phone is skipped.
	if _, ok := backfillMember(context.Background(), nil, nil, types.GroupParticipant{}); ok {
		t.Fatal("expected skip for a participant with no LID/phone")
	}
}
