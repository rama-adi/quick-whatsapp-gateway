// The session-lifecycle facade adapts the private engine client onto the
// SessionService's resolved lifecycle port (Increment 7). Row, placement,
// assignment and intent ownership stays with the API; this type only re-shapes
// the live engine calls (apply, QR read, phone pairing, passkey, logout,
// forget) onto org/session signatures. Logout is a durable ledgered command;
// the rest are idempotent or read-classified on the wire.
package gateway

import (
	"context"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
)

// SessionLifecycleFacade implements the service package's GatewaySessionFacade
// over one EngineClient.
type SessionLifecycleFacade struct {
	client *EngineClient
}

// NewSessionLifecycleFacade wraps an engine client for the session service.
func NewSessionLifecycleFacade(client *EngineClient) *SessionLifecycleFacade {
	return &SessionLifecycleFacade{client: client}
}

// Apply applies a session's recorded intent on its gateway now.
func (f *SessionLifecycleFacade) Apply(
	ctx context.Context,
	organizationID, sessionID string,
	run bool,
	startGeneration uint64,
	deviceJID string,
) error {
	return f.client.ApplySession(ctx, organizationID, sessionID, run, startGeneration, deviceJID)
}

// QR reads the current pairing QR code. An empty Code means no code is ready
// yet — the caller polls again or subscribes to auth.qr events.
func (f *SessionLifecycleFacade) QR(
	ctx context.Context,
	organizationID string,
	sessionID string,
) (application.PairingSnapshot, error) {
	return f.client.GetPairingQR(ctx, organizationID, sessionID)
}

// PairingCode requests a phone-number pairing code through the assigned engine.
func (f *SessionLifecycleFacade) PairingCode(
	ctx context.Context,
	organizationID string,
	sessionID string,
	phone string,
) (string, error) {
	return f.client.PairPhone(ctx, organizationID, sessionID, phone)
}

// SubmitPasskey forwards the user's WebAuthn assertion for a pending passkey
// verification.
func (f *SessionLifecycleFacade) SubmitPasskey(ctx context.Context, organizationID, sessionID string, response []byte) error {
	return f.client.SubmitPasskey(ctx, organizationID, sessionID, response)
}

// ConfirmPasskey confirms the passkey pairing code the user compared with the phone.
func (f *SessionLifecycleFacade) ConfirmPasskey(ctx context.Context, organizationID, sessionID string) error {
	return f.client.ConfirmPasskey(ctx, organizationID, sessionID)
}

// Logout unlinks the device as a durable ledgered command.
func (f *SessionLifecycleFacade) Logout(ctx context.Context, organizationID, sessionID string) error {
	return f.client.LogoutSession(ctx, organizationID, sessionID)
}

// Forget drops the session's in-memory runtime during the API-owned delete
// flow. Idempotent by contract.
func (f *SessionLifecycleFacade) Forget(ctx context.Context, organizationID, sessionID string) error {
	return f.client.ForgetSession(ctx, organizationID, sessionID)
}
