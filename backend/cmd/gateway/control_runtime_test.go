package main

import (
	"context"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/controlsupervisor"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/gateway/desiredstate"
)

type fixedControlStatus struct{ status controlsupervisor.Status }

func (s fixedControlStatus) Status() controlsupervisor.Status { return s.status }

type controlCountRuntime struct{}

func (*controlCountRuntime) Inventory(context.Context) (desiredstate.Inventory, error) {
	return desiredstate.Inventory{PairedJIDs: []string{"one", "two"}}, nil
}
func (*controlCountRuntime) StartAssigned(context.Context, desiredstate.Assignment) error { return nil }
func (*controlCountRuntime) StopAssigned(context.Context, string) error                   { return nil }

func TestCertificateRenewalDeadlineUsesCertificateExpiryAndConfiguredWindow(t *testing.T) {
	expiry := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	if got, want := renewalDeadline(expiry, 90*time.Minute), expiry.Add(-90*time.Minute); !got.Equal(want) {
		t.Fatalf("renewal deadline = %s, want %s", got, want)
	}
}

// The legacy registry (registerGateway / heartbeat / SetStatus writes) was
// deleted with the gateway's MySQL access. None of its mutation sites may
// reappear in the composition root.
