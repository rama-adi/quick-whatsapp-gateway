package wa

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func startTestPairing(t *testing.T) (*Manager, *fakeSink, *fakeClient, *ManagedSession) {
	t.Helper()
	manager, sink, _, client := newTestManagerParts(t, Config{})
	if err := manager.StartAssigned(context.Background(), runAssignment("session", "", 1)); err != nil {
		t.Fatal(err)
	}
	ms := manager.Get("session")
	return manager, sink, client, ms
}

func eventsOfType(sink *fakeSink, eventType string) []domain.Event {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var out []domain.Event
	for _, e := range sink.events {
		if e.Type == eventType {
			out = append(out, e)
		}
	}
	return out
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestQREventPublishesCodeAndScanStatus(t *testing.T) {
	manager, sink, _, ms := startTestPairing(t)
	if ms.Status() == domain.SessionScanQR {
		t.Fatal("scan_qr_code published before any code existed")
	}
	manager.handlePairingEvent(ms, currentRun(ms), &events.QR{Codes: []string{"c1", "c2", "c3", "c4", "c5", "c6"}})
	waitFor(t, func() bool { return len(eventsOfType(sink, domain.EventAuthQR)) == 1 })
	if code, _ := manager.LatestQR("session"); code != "c1" {
		t.Fatalf("latest QR = %q, want c1", code)
	}
	if ms.Status() != domain.SessionScanQR {
		t.Fatalf("status = %q, want scan_qr_code", ms.Status())
	}
}

func TestPasskeyRequestStopsQRAndPublishesOptions(t *testing.T) {
	manager, sink, client, ms := startTestPairing(t)
	run := currentRun(ms)
	manager.handlePairingEvent(ms, run, &events.QR{Codes: []string{"c1", "c2", "c3", "c4", "c5", "c6"}})
	waitFor(t, func() bool { code, _ := manager.LatestQR("session"); return code == "c1" })

	options := &types.WebAuthnPublicKey{Challenge: []byte("challenge"), RelyingPartID: "whatsapp.com", Timeout: 60000}
	manager.handlePairingEvent(ms, run, &events.PairPasskeyRequest{PublicKey: options})

	passkeyEvents := eventsOfType(sink, domain.EventAuthPasskey)
	if len(passkeyEvents) != 1 {
		t.Fatalf("auth.passkey events = %d, want 1", len(passkeyEvents))
	}
	payload := passkeyEvents[0].Payload.(map[string]any)
	if payload["step"] != "verify" || payload["publicKey"] != options {
		t.Fatalf("auth.passkey payload = %+v", payload)
	}
	if code, _ := manager.LatestQR("session"); code != "" {
		t.Fatalf("QR code %q still served while a passkey verification is pending", code)
	}

	// Running out of QR codes must not end a pending passkey verification.
	manager.emitQRCodes(run.ctx, ms, run, nil)
	if currentRun(ms) != run || ms.Status() == domain.SessionFailed {
		t.Fatal("QR exhaustion tore down a pending passkey verification")
	}

	// The assertion is forwarded to WhatsApp as-is.
	assertion, _ := json.Marshal(map[string]any{
		"id": "cred", "rawId": "Y3JlZA", "type": "public-key",
		"response": map[string]any{"clientDataJSON": "e30", "authenticatorData": "AA", "signature": "AQ"},
	})
	if err := manager.SubmitPasskey(context.Background(), "session", assertion); err != nil {
		t.Fatal(err)
	}
	if len(client.passkeyResponses) != 1 || string(client.passkeyResponses[0].RawID) != "cred" {
		t.Fatalf("passkey responses = %+v", client.passkeyResponses)
	}

	// Confirmation is only accepted once WhatsApp sent a code to compare.
	if err := manager.ConfirmPasskey(context.Background(), "session"); err == nil {
		t.Fatal("confirmation accepted before a code was shown")
	}
	manager.handlePairingEvent(ms, run, &events.PairPasskeyConfirmation{Code: "ABCD-EFGH"})
	confirm := eventsOfType(sink, domain.EventAuthPasskey)
	if len(confirm) != 2 || confirm[1].Payload.(map[string]any)["code"] != "ABCD-EFGH" {
		t.Fatalf("confirm event = %+v", confirm)
	}
	if err := manager.ConfirmPasskey(context.Background(), "session"); err != nil {
		t.Fatal(err)
	}
	if client.passkeyConfirms != 1 {
		t.Fatalf("passkey confirmations = %d, want 1", client.passkeyConfirms)
	}
}

func TestPasskeySkipHandoffConfirmsAutomatically(t *testing.T) {
	manager, sink, client, ms := startTestPairing(t)
	run := currentRun(ms)
	manager.handlePairingEvent(ms, run, &events.PairPasskeyRequest{PublicKey: &types.WebAuthnPublicKey{}})
	manager.handlePairingEvent(ms, run, &events.PairPasskeyConfirmation{Code: "ABCD-EFGH", SkipHandoffUX: true})
	waitFor(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.passkeyConfirms == 1
	})
	if n := len(eventsOfType(sink, domain.EventAuthPasskey)); n != 1 {
		t.Fatalf("auth.passkey events = %d; a skipped handoff must not ask the user", n)
	}
}

func TestSubmitPasskeyRejectsWithoutPendingVerification(t *testing.T) {
	manager, _, _, _ := startTestPairing(t)
	assertion := []byte(`{"id":"c","rawId":"Yw","type":"public-key","response":{"clientDataJSON":"e30","authenticatorData":"AA","signature":"AQ"}}`)
	if err := manager.SubmitPasskey(context.Background(), "session", assertion); err == nil {
		t.Fatal("assertion accepted with no pending passkey verification")
	}
	if err := manager.SubmitPasskey(context.Background(), "session", []byte(`{"id":"c"}`)); err == nil {
		t.Fatal("incomplete assertion accepted")
	}
}

func TestPasskeyErrorFailsPairing(t *testing.T) {
	manager, _, _, ms := startTestPairing(t)
	run := currentRun(ms)
	manager.handlePairingEvent(ms, run, &events.PairPasskeyRequest{PublicKey: &types.WebAuthnPublicKey{}})
	manager.applyEvent(context.Background(), ms, run, &events.PairPasskeyError{})
	ms.mu.Lock()
	reason, conn, pending := ms.failure, ms.conn, ms.passkey
	ms.mu.Unlock()
	if reason != domain.FailurePasskeyFailed || conn != nil || pending != nil {
		t.Fatalf("after passkey error: reason=%q run=%v pending=%v", reason, conn, pending)
	}
}

func TestPasskeyWindowBounds(t *testing.T) {
	if got := passkeyWindow(&types.WebAuthnPublicKey{Timeout: 1000}); got != minPasskeyWindow {
		t.Fatalf("short timeout window = %v", got)
	}
	if got := passkeyWindow(&types.WebAuthnPublicKey{Timeout: 3_600_000}); got != maxPasskeyWindow {
		t.Fatalf("long timeout window = %v", got)
	}
	if got := passkeyWindow(&types.WebAuthnPublicKey{Timeout: 300_000}); got != 5*time.Minute {
		t.Fatalf("5 minute timeout window = %v", got)
	}
}

// Regression (live, 2026-11-23): after a passkey pairing succeeded, the QR
// emitter kept running, published scan_qr_code over the working session and,
// once its codes ran out, tore the paired client down with qr_timeout.
func TestPairSuccessStopsQREmitterWithoutTeardown(t *testing.T) {
	manager, sink, _, ms := startTestPairing(t)
	run := currentRun(ms)
	manager.handlePairingEvent(ms, run, &events.QR{Codes: []string{"c1", "c2"}})
	waitFor(t, func() bool { return len(eventsOfType(sink, domain.EventAuthQR)) == 1 })

	manager.applyEvent(context.Background(), ms, run, &events.PairSuccess{ID: types.NewJID("6281", types.DefaultUserServer)})
	manager.applyEvent(context.Background(), ms, run, &events.Connected{})
	time.Sleep(20 * time.Millisecond)

	if ms.Status() != domain.SessionWorking {
		t.Fatalf("status = %q, want working", ms.Status())
	}
	if code, _ := manager.LatestQR("session"); code != "" {
		t.Fatalf("QR code %q still served after pairing", code)
	}
	// Running out of codes after pairing must not tear the client down.
	manager.emitQRCodes(run.ctx, ms, run, nil)
	if currentRun(ms) != run || ms.Status() != domain.SessionWorking {
		t.Fatal("QR exhaustion after pairing tore down the working session")
	}
	if n := len(eventsOfType(sink, domain.EventAuthQR)); n != 1 {
		t.Fatalf("auth.qr events = %d after pairing; want no more codes", n)
	}
}
