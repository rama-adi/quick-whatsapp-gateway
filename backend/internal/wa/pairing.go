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
//     session with qr_timeout once they run out. A reconnect of the pairing
//     run brings new codes and replaces the emitter.
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

// handlePairingEvent drives QR rotation and the passkey flow for run c. It
// reports whether evt was a pairing event it consumed.
func (m *Manager) handlePairingEvent(ms *ManagedSession, c *connection, evt any) bool {
	switch e := evt.(type) {
	case *events.QR:
		// A reconnect of an unpaired run brings a new set of codes; the
		// emitter of the previous set stops.
		ctx, cancel := context.WithCancel(c.ctx)
		ms.mu.Lock()
		if ms.conn != c {
			ms.mu.Unlock()
			cancel()
			return true
		}
		if c.qrCancel != nil {
			c.qrCancel()
		}
		c.qrCancel = cancel
		// New codes prove the pairing connection is up again; an unpaired
		// run gets no Connected event to say so.
		c.down = false
		ms.attempt, ms.lastErr = 0, ""
		ms.mu.Unlock()
		go m.emitQRCodes(ctx, ms, c, append([]string(nil), e.Codes...))
		return true
	case *events.RotateADVSecret:
		select {
		case c.qrRotate <- e:
		default:
		}
		return true
	case *events.PairPasskeyRequest:
		m.startPasskey(ms, c, e.PublicKey)
		return true
	case *events.PairPasskeyConfirmation:
		m.passkeyConfirmation(ms, c, e)
		return true
	case *events.QRScannedWithoutMultidevice:
		m.log.Warn("qr scanned by a phone without multi-device; the same code can be scanned again", "session", ms.SessionID)
		return true
	}
	return false
}

// emitQRCodes publishes the codes of one events.QR in turn. It stops when ctx
// ends (the run ended or a newer set of codes arrived), when a passkey
// verification starts, or when the codes run out; in the last case the run
// fails with qr_timeout.
func (m *Manager) emitQRCodes(ctx context.Context, ms *ManagedSession, c *connection, codes []string) {
	for i := 0; i < len(codes); i++ {
		timeout := nextQRTimeout
		if i == 0 && len(codes) == 6 {
			timeout = firstQRTimeout
		}
		if !m.publishQR(ctx, ms, c, codes[i], timeout) {
			return
		}
		timer := time.NewTimer(timeout)
	wait:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case rot := <-c.qrRotate:
				for j := i; j < len(codes); j++ {
					codes[j] = strings.Replace(codes[j], rot.OldSecret, rot.NewSecret, 1)
				}
				if !m.publishQR(ctx, ms, c, codes[i], timeout) {
					timer.Stop()
					return
				}
			case <-timer.C:
				break wait
			}
		}
	}
	ms.mu.Lock()
	// A passkey verification or a successful pairing owns the run now. Left
	// unchecked, running out of codes after PairSuccess tore the working client
	// down with qr_timeout (live, 2026-11-23).
	ended := ms.passkey != nil || ms.pairedJID != "" || (ms.device != nil && ms.device.ID != nil)
	ms.mu.Unlock()
	if ended || ctx.Err() != nil {
		return
	}
	if m.endRun(c.ctx, ms, c, domain.SessionFailed, domain.FailureQRTimeout, false) {
		m.log.Warn("qr pairing timed out", "session", ms.SessionID)
	}
}

// publishQR stores code for GET /qr, moves the status to scan_qr_code and
// publishes auth.qr. It returns false when the run is over or a passkey
// verification has taken over.
func (m *Manager) publishQR(ctx context.Context, ms *ManagedSession, c *connection, code string, timeout time.Duration) bool {
	ms.mu.Lock()
	if ctx.Err() != nil || ms.conn != c || ms.passkey != nil {
		ms.mu.Unlock()
		return false
	}
	ms.lastQR = code
	ms.lastQRExpires = m.clock.NowMs() + timeout.Milliseconds()
	ms.mu.Unlock()
	// scan_qr_code means "a code exists to scan": publish it only after the
	// code is stored, so GET /qr answers with it.
	if !m.setState(ctx, ms, c, domain.SessionScanQR, "") {
		return false
	}
	m.publish(ctx, domain.NewEvent(domain.EventAuthQR, ms.SessionID, ms.OrganizationID, map[string]any{
		"code":      code,
		"timeoutMs": timeout.Milliseconds(),
	}))
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
func (m *Manager) startPasskey(ms *ManagedSession, c *connection, options *types.WebAuthnPublicKey) {
	window := passkeyWindow(options)
	pending := &pendingPasskey{options: options, expiresAt: m.clock.NowMs() + window.Milliseconds()}
	ctx := c.ctx
	ms.mu.Lock()
	if ms.conn != c || ctx.Err() != nil {
		ms.mu.Unlock()
		return
	}
	ms.passkey = pending
	ms.lastQR, ms.lastQRExpires = "", 0
	if c.qrCancel != nil {
		c.qrCancel()
		c.qrCancel = nil
	}
	ms.mu.Unlock()

	m.log.Info("passkey verification requested", "session", ms.SessionID, "rp_id", rpID(options), "window", window.String())
	m.publishPasskey(ctx, ms, map[string]any{
		"step":      "verify",
		"publicKey": options,
		"expiresAt": pending.expiresAt,
	})
	go m.expirePasskey(ms, c, pending, window)
}

func rpID(options *types.WebAuthnPublicKey) string {
	if options == nil {
		return ""
	}
	return options.RelyingPartID
}

// expirePasskey fails the pairing if this verification is still pending at
// its deadline.
func (m *Manager) expirePasskey(ms *ManagedSession, c *connection, pending *pendingPasskey, window time.Duration) {
	timer := time.NewTimer(window)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		return
	case <-timer.C:
	}
	ms.mu.Lock()
	still := ms.conn == c && ms.passkey == pending
	ms.mu.Unlock()
	if !still {
		return
	}
	m.log.Warn("passkey verification timed out", "session", ms.SessionID)
	m.endRun(c.ctx, ms, c, domain.SessionFailed, domain.FailurePasskeyFailed, false)
}

// passkeyConfirmation handles WhatsApp's request to compare a code with the
// phone after the assertion was accepted.
func (m *Manager) passkeyConfirmation(ms *ManagedSession, c *connection, evt *events.PairPasskeyConfirmation) {
	ctx, client := c.ctx, c.client
	ms.mu.Lock()
	pending := ms.passkey
	if ms.conn != c {
		pending = nil
	}
	if pending != nil {
		pending.confirmCode = evt.Code
	}
	ms.mu.Unlock()
	if ctx.Err() != nil || pending == nil {
		return
	}
	if evt.SkipHandoffUX {
		// WhatsApp says the code comparison can be skipped (the QR scan already
		// proved the handoff), so confirm right away. Run it off the event
		// goroutine: the confirmation's reply arrives on that same connection.
		go func() {
			if err := client.SendPasskeyConfirmation(ctx); err != nil && ctx.Err() == nil {
				m.log.Warn("automatic passkey confirmation failed", "session", ms.SessionID, "err", err)
				m.endRun(ctx, ms, c, domain.SessionFailed, domain.FailurePasskeyFailed, false)
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
	m.publish(ctx, domain.NewEvent(domain.EventAuthPasskey, ms.SessionID, ms.OrganizationID, payload))
}

// pairingClient returns the client of the running pairing and its pending
// passkey verification, if any.
func (ms *ManagedSession) pairingClient() (waClient, *pendingPasskey) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.conn == nil {
		return nil, nil
	}
	return ms.conn.client, ms.passkey
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
	client, pending := ms.pairingClient()
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
	client, pending := ms.pairingClient()
	code := ""
	if pending != nil {
		ms.mu.Lock()
		code = pending.confirmCode
		ms.mu.Unlock()
	}
	if client == nil || pending == nil || code == "" {
		return domain.ErrConflict("no passkey code is waiting for confirmation")
	}
	if err := client.SendPasskeyConfirmation(ctx); err != nil {
		return fmt.Errorf("send passkey confirmation: %w", err)
	}
	return nil
}
