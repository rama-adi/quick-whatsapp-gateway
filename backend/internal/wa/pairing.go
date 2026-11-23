package wa

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// Pairing driver.
//
// The gateway does not use whatsmeow's GetQRChannel. That channel disconnects
// the client as soon as its QR codes run out, which kills a passkey
// verification still in progress, and it auto-confirms passkey pairings. The
// manager instead reacts to the raw pairing events:
//
//   - events.QR starts emitQRCodes, which publishes each code as auth.qr
//     (60s for the first, 20s for the rest, like whatsmeow) and fails the
//     session with qr_timeout once they run out.
//   - events.PairPasskeyRequest stops the QR codes (WhatsApp rotates the ADV
//     secret, so the old codes are dead) and publishes auth.passkey with the
//     WebAuthn request options. The user signs them on web.whatsapp.com and
//     the API forwards the assertion to SubmitPasskey.
//   - events.PairPasskeyConfirmation asks the user to compare a code with the
//     phone (auth.passkey step confirm), unless WhatsApp says the handoff UX
//     can be skipped, in which case the gateway confirms by itself.
//   - events.PairPasskeyError fails the session with passkey_failed.

const (
	firstQRTimeout = 60 * time.Second
	nextQRTimeout  = 20 * time.Second
	// minPasskeyWindow is the least time the user gets to finish a passkey
	// verification, whatever timeout WhatsApp puts in the WebAuthn options.
	minPasskeyWindow = 3 * time.Minute
	maxPasskeyWindow = 10 * time.Minute
)

// beginPairingRun records the context of a new pairing run. Call with ms.mu held.
func (ms *ManagedSession) beginPairingRun(ctx context.Context) {
	ms.pairCtx = ctx
	ms.qrRotate = make(chan *events.RotateADVSecret, 4)
	ms.qrStop = make(chan struct{})
	ms.qrStopped = false
	ms.passkey = nil
	ms.lastQR = ""
	ms.lastQRExpires = 0
}

// stopQRCodes ends this run's QR codes for good. Call with ms.mu held. The QR
// emitter must not outlive a passkey verification or a successful pairing:
// it would publish scan_qr_code over a working session and, once its codes ran
// out, tear the paired client down with qr_timeout.
func (ms *ManagedSession) stopQRCodes() {
	if ms.qrStop != nil && !ms.qrStopped {
		close(ms.qrStop)
		ms.qrStopped = true
	}
	ms.lastQR, ms.lastQRExpires = "", 0
}

// handlePairingEvent drives QR rotation and the passkey flow. It reports
// whether evt was a pairing event it consumed.
func (m *Manager) handlePairingEvent(ms *ManagedSession, evt any) bool {
	switch e := evt.(type) {
	case *events.QR:
		ms.mu.Lock()
		ctx, rotate, stop, stopped := ms.pairCtx, ms.qrRotate, ms.qrStop, ms.qrStopped
		ms.mu.Unlock()
		if ctx == nil || ctx.Err() != nil || stopped {
			return true
		}
		go m.emitQRCodes(ctx, ms, append([]string(nil), e.Codes...), rotate, stop)
		return true
	case *events.RotateADVSecret:
		ms.mu.Lock()
		rotate := ms.qrRotate
		ms.mu.Unlock()
		if rotate != nil {
			select {
			case rotate <- e:
			default:
			}
		}
		return true
	case *events.PairPasskeyRequest:
		m.startPasskey(ms, e.PublicKey)
		return true
	case *events.PairPasskeyConfirmation:
		m.passkeyConfirmation(ms, e)
		return true
	case *events.QRScannedWithoutMultidevice:
		m.log.Warn("qr scanned by a phone without multi-device; the same code can be scanned again", "session", ms.SessionID)
		return true
	}
	return false
}

// emitQRCodes publishes the codes of one events.QR in turn. It stops when the
// run ends, when a passkey verification starts, or when the codes run out; in
// the last case the session fails with qr_timeout.
func (m *Manager) emitQRCodes(ctx context.Context, ms *ManagedSession, codes []string, rotate <-chan *events.RotateADVSecret, stop <-chan struct{}) {
	for i := 0; i < len(codes); i++ {
		timeout := nextQRTimeout
		if i == 0 && len(codes) == 6 {
			timeout = firstQRTimeout
		}
		if !m.publishQR(ctx, ms, codes[i], timeout) {
			return
		}
		timer := time.NewTimer(timeout)
	wait:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-stop:
				timer.Stop()
				return
			case rot := <-rotate:
				for j := i; j < len(codes); j++ {
					codes[j] = strings.Replace(codes[j], rot.OldSecret, rot.NewSecret, 1)
				}
				if !m.publishQR(ctx, ms, codes[i], timeout) {
					timer.Stop()
					return
				}
			case <-timer.C:
				break wait
			}
		}
	}
	ms.mu.Lock()
	ended := ms.passkey != nil || ms.qrStopped || ms.pairedJID != "" || (ms.device != nil && ms.device.ID != nil)
	ms.mu.Unlock()
	if ended || ctx.Err() != nil {
		// A passkey verification or a successful pairing owns the run now.
		return
	}
	m.log.Warn("qr pairing timed out", "session", ms.SessionID)
	m.teardown(ms)
	m.setFailed(ctx, ms, domain.FailureQRTimeout)
}

// publishQR stores code for GET /qr, moves the status to scan_qr_code and
// publishes auth.qr. It returns false when the run is over or a passkey
// verification has taken over.
func (m *Manager) publishQR(ctx context.Context, ms *ManagedSession, code string, timeout time.Duration) bool {
	ms.mu.Lock()
	if ctx.Err() != nil || ms.passkey != nil || ms.qrStopped {
		ms.mu.Unlock()
		return false
	}
	ms.lastQR = code
	ms.lastQRExpires = m.clock.NowMs() + timeout.Milliseconds()
	ms.mu.Unlock()
	// scan_qr_code means "a code exists to scan": publish it only after the
	// code is stored, so GET /qr answers with it.
	m.setStatus(ctx, ms, domain.SessionScanQR)
	if m.sink != nil {
		m.sink.Publish(ctx, domain.NewEvent(domain.EventAuthQR, ms.SessionID, ms.OrganizationID, map[string]any{
			"code":      code,
			"timeoutMs": timeout.Milliseconds(),
		}))
	}
	return true
}

// passkeyWindow is how long the user gets to finish a passkey verification.
func passkeyWindow(options *types.WebAuthnPublicKey) time.Duration {
	window := minPasskeyWindow
	if options != nil {
		if d := time.Duration(options.Timeout) * time.Millisecond; d > window {
			window = d
		}
	}
	return min(window, maxPasskeyWindow)
}

// startPasskey records WhatsApp's passkey request, stops the QR codes and
// publishes the WebAuthn options for the user to sign.
func (m *Manager) startPasskey(ms *ManagedSession, options *types.WebAuthnPublicKey) {
	window := passkeyWindow(options)
	pending := &pendingPasskey{options: options, expiresAt: m.clock.NowMs() + window.Milliseconds()}
	ms.mu.Lock()
	ctx := ms.pairCtx
	if ctx == nil || ctx.Err() != nil {
		ms.mu.Unlock()
		return
	}
	ms.passkey = pending
	ms.stopQRCodes()
	ms.mu.Unlock()

	m.log.Info("passkey verification requested", "session", ms.SessionID, "rp_id", rpID(options), "window", window.String())
	m.publishPasskey(ctx, ms, map[string]any{
		"step":      "verify",
		"publicKey": options,
		"expiresAt": pending.expiresAt,
	})
	go m.expirePasskey(ctx, ms, pending, window)
}

func rpID(options *types.WebAuthnPublicKey) string {
	if options == nil {
		return ""
	}
	return options.RelyingPartID
}

// expirePasskey fails the pairing if this verification is still pending at
// its deadline.
func (m *Manager) expirePasskey(ctx context.Context, ms *ManagedSession, pending *pendingPasskey, window time.Duration) {
	timer := time.NewTimer(window)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	ms.mu.Lock()
	still := ms.passkey == pending
	ms.mu.Unlock()
	if !still {
		return
	}
	m.log.Warn("passkey verification timed out", "session", ms.SessionID)
	m.teardown(ms)
	m.setFailed(ctx, ms, domain.FailurePasskeyFailed)
}

// passkeyConfirmation handles WhatsApp's request to compare a code with the
// phone after the assertion was accepted.
func (m *Manager) passkeyConfirmation(ms *ManagedSession, evt *events.PairPasskeyConfirmation) {
	ms.mu.Lock()
	ctx, client, pending := ms.pairCtx, ms.client, ms.passkey
	if pending != nil {
		pending.confirmCode = evt.Code
	}
	ms.mu.Unlock()
	if ctx == nil || ctx.Err() != nil || client == nil || pending == nil {
		return
	}
	if evt.SkipHandoffUX {
		// WhatsApp says the code comparison can be skipped (the QR scan already
		// proved the handoff), so confirm right away. Run it off the event
		// goroutine: the confirmation's reply arrives on that same connection.
		go func() {
			if err := client.SendPasskeyConfirmation(ctx); err != nil && ctx.Err() == nil {
				m.log.Warn("automatic passkey confirmation failed", "session", ms.SessionID, "err", err)
				m.teardown(ms)
				m.setFailed(ctx, ms, domain.FailurePasskeyFailed)
			}
		}()
		return
	}
	m.publishPasskey(ctx, ms, map[string]any{
		"step":      "confirm",
		"code":      evt.Code,
		"expiresAt": pending.expiresAt,
	})
}

func (m *Manager) publishPasskey(ctx context.Context, ms *ManagedSession, payload map[string]any) {
	if m.sink != nil {
		m.sink.Publish(context.WithoutCancel(ctx), domain.NewEvent(domain.EventAuthPasskey, ms.SessionID, ms.OrganizationID, payload))
	}
}

// SubmitPasskey forwards the user's WebAuthn assertion for the pending passkey
// verification to WhatsApp. response is the PublicKeyCredential JSON (id,
// rawId, type, response{clientDataJSON, authenticatorData, signature,
// userHandle}) with base64url fields. A rejected assertion leaves the
// verification pending, so the user can try again before the deadline.
func (m *Manager) SubmitPasskey(ctx context.Context, id string, response []byte) error {
	ms := m.Get(id)
	if ms == nil {
		return domain.ErrNotFound("session not found")
	}
	var assertion types.WebAuthnResponse
	if err := json.Unmarshal(response, &assertion); err != nil {
		return domain.ErrValidation("passkey response is not a WebAuthn assertion: " + err.Error())
	}
	if len(assertion.RawID) == 0 || len(assertion.Response.Signature) == 0 || len(assertion.Response.ClientDataJSON) == 0 {
		return domain.ErrValidation("passkey response is missing rawId, signature or clientDataJSON")
	}
	ms.mu.Lock()
	client, pending := ms.client, ms.passkey
	ms.mu.Unlock()
	if client == nil || pending == nil {
		return domain.ErrConflict("no passkey verification is pending for this session")
	}
	if err := client.SendPasskeyResponse(ctx, &assertion); err != nil {
		m.log.Warn("passkey response rejected", "session", ms.SessionID, "err", err)
		return fmt.Errorf("send passkey response: %w", err)
	}
	m.log.Info("passkey response accepted", "session", ms.SessionID)
	return nil
}

// ConfirmPasskey tells WhatsApp the user saw the same code on the phone.
func (m *Manager) ConfirmPasskey(ctx context.Context, id string) error {
	ms := m.Get(id)
	if ms == nil {
		return domain.ErrNotFound("session not found")
	}
	ms.mu.Lock()
	client, pending := ms.client, ms.passkey
	code := ""
	if pending != nil {
		code = pending.confirmCode
	}
	ms.mu.Unlock()
	if client == nil || pending == nil || code == "" {
		return domain.ErrConflict("no passkey code is waiting for confirmation")
	}
	if err := client.SendPasskeyConfirmation(ctx); err != nil {
		return fmt.Errorf("send passkey confirmation: %w", err)
	}
	return nil
}
