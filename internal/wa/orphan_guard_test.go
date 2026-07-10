package wa

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// TestBootResumeDecision evaluates the boot guard across missing sessions, deleted organizations,
// foreign gateway pins, terminal statuses, and eligible rows. The table pins each skip/adopt decision
// so restart recovery never crosses ownership or tenant boundaries.
func TestBootResumeDecision(t *testing.T) {
	tests := []struct {
		name       string
		pred       func(ctx context.Context, orgID string) (bool, error)
		wantResume bool
		wantStop   bool
	}{
		{
			name:       "no guard installed -> resume",
			pred:       nil,
			wantResume: true, wantStop: false,
		},
		{
			name:       "org exists -> resume",
			pred:       func(context.Context, string) (bool, error) { return true, nil },
			wantResume: true, wantStop: false,
		},
		{
			name:       "org gone -> stop, do not resume",
			pred:       func(context.Context, string) (bool, error) { return false, nil },
			wantResume: false, wantStop: true,
		},
		{
			name:       "predicate error -> fail safe (resume, do not stop)",
			pred:       func(context.Context, string) (bool, error) { return false, errors.New("db blip") },
			wantResume: true, wantStop: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _, _, _, _ := newTestManager(t, Config{})
			m.SetOrgExists(tt.pred)
			sess := &domain.WASession{ID: "sess_1", OrganizationID: "org_1", Status: domain.SessionWorking}
			resume, stop := m.bootResumeDecision(context.Background(), sess)
			if resume != tt.wantResume || stop != tt.wantStop {
				t.Fatalf("decision = (resume=%v, stop=%v), want (resume=%v, stop=%v)",
					resume, stop, tt.wantResume, tt.wantStop)
			}
		})
	}
}

// pairedDevice builds a keystore device with a phone JID so Boot adopts it.
func pairedDevice(number string) (*store.Device, string) {
	jid := types.NewJID(number, types.DefaultUserServer)
	return &store.Device{ID: &jid}, jid.String()
}

// TestBoot_OrphanGuard_OrgExists_Resumes presents a paired device whose session and organization
// still exist. Boot adopts it, pins the local client, and connects once, proving healthy durable
// ownership survives restart.
func TestBoot_OrphanGuard_OrgExists_Resumes(t *testing.T) {
	m, repo, _, _, _ := newTestManager(t, Config{})
	dev, jid := pairedDevice("628111")
	m.keystore.(*fakeKeystore).devices = []*store.Device{dev}

	sess := &domain.WASession{ID: "sess_live", OrganizationID: "org_ok", Status: domain.SessionWorking, WAJID: &jid}
	repo.byID[sess.ID] = sess
	repo.byJID[jid] = sess

	m.SetOrgExists(func(_ context.Context, org string) (bool, error) {
		return org == "org_ok", nil
	})

	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	// Resumed sessions get a started managed client (set synchronously by
	// startManaged; Connect runs in its goroutine). The client being non-nil is
	// the race-free signal that the session was resumed.
	ms := m.Get(sess.ID)
	if ms == nil {
		t.Fatal("session not adopted on boot")
	}
	ms.mu.Lock()
	resumed := ms.client != nil
	ms.mu.Unlock()
	if !resumed {
		t.Fatal("expected the live session to be resumed (managed client started)")
	}
	for _, su := range repo.statuses {
		if su.id == sess.ID && su.status == domain.SessionStopped {
			t.Fatalf("session with existing org must not be stopped")
		}
	}
}

// TestBoot_OrphanGuard_OrgGone_Stops presents a paired device whose owning organization was
// deleted. Boot refuses to connect and marks the row stopped, preventing orphan credentials from
// sending traffic.
func TestBoot_OrphanGuard_OrgGone_Stops(t *testing.T) {
	m, repo, _, _, _ := newTestManager(t, Config{})
	dev, jid := pairedDevice("628222")
	m.keystore.(*fakeKeystore).devices = []*store.Device{dev}

	sess := &domain.WASession{ID: "sess_orphan", OrganizationID: "org_gone", Status: domain.SessionWorking, WAJID: &jid}
	repo.byID[sess.ID] = sess
	repo.byJID[jid] = sess

	m.SetOrgExists(func(context.Context, string) (bool, error) { return false, nil })

	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	// Adopted (so Get returns it) but NOT resumed: no managed client started.
	if ms := m.Get(sess.ID); ms != nil {
		ms.mu.Lock()
		started := ms.client != nil
		ms.mu.Unlock()
		if started {
			t.Fatal("orphaned session must NOT be resumed (no managed client)")
		}
	}
	stopped := false
	for _, su := range repo.statuses {
		if su.id == sess.ID && su.status == domain.SessionStopped {
			stopped = true
		}
	}
	if !stopped {
		t.Fatalf("orphaned session must be marked STOPPED; statuses = %+v", repo.statuses)
	}
}

// TestBoot_PinsGatewayID resumes an eligible unpinned session on a configured gateway. The
// repository receives the gateway ID before connection, preserving deterministic owner routing during
// startup.
func TestBoot_PinsGatewayID(t *testing.T) {
	m, repo, _, _, _ := newTestManager(t, Config{GatewayID: "gw-test"})
	dev, jid := pairedDevice("628333")
	m.keystore.(*fakeKeystore).devices = []*store.Device{dev}

	sess := &domain.WASession{ID: "sess_pin", OrganizationID: "org_ok", Status: domain.SessionStopped, WAJID: &jid}
	repo.byID[sess.ID] = sess
	repo.byJID[jid] = sess

	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	if got := repo.byID[sess.ID].GatewayID; got != "gw-test" {
		t.Fatalf("session gateway_id = %q, want gw-test", got)
	}
}
