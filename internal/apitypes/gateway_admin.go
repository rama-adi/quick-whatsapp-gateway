package apitypes

import (
	"encoding/json"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// GatewayAdmin is the complete non-secret gateway registry projection exposed to
// platform operators. It intentionally omits enrollment-token hashes, PEM/CSR
// material, and other credential data.
type GatewayAdmin struct {
	ID                   string          `json:"id" example:"gw_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	Label                *string         `json:"label,omitempty" example:"Singapore primary"`
	Notes                *string         `json:"notes,omitempty" example:"Runs the APAC production pool."`
	Status               string          `json:"status" enum:"pending_enrollment,joining,active,draining,drained,degraded,disabled" example:"active"`
	SessionCount         int             `json:"sessionCount" example:"12"`
	Capacity             *int            `json:"capacity,omitempty" example:"100"`
	BaseURL              *string         `json:"baseUrl,omitempty" example:"https://gw-sg-1.example.com"`
	GRPCEndpoint         *string         `json:"grpcEndpoint,omitempty" example:"gw-sg-1.internal:8443"`
	SoftwareVersion      *string         `json:"softwareVersion,omitempty" example:"2.0.0"`
	Capabilities         json.RawMessage `json:"capabilities,omitempty" additionalProperties:"true"`
	ConnectionEpoch      uint64          `json:"connectionEpoch" example:"4"`
	ConnectionMode       string          `json:"connectionMode" enum:"legacy,control" example:"control"`
	DesiredLifecycle     string          `json:"desiredLifecycle" enum:"run,drain" example:"run"`
	DesiredRevision      uint64          `json:"desiredRevision" example:"5"`
	AppliedRevision      uint64          `json:"appliedRevision" example:"5"`
	ReconciliationStatus string          `json:"reconciliationStatus" enum:"pending,healthy,degraded" example:"healthy"`
	KeystorePresent      *bool           `json:"keystorePresent,omitempty" example:"true"`
	KeystoreBytes        *int64          `json:"keystoreBytes,omitempty" example:"10485760"`
	KeystoreIntegrity    *string         `json:"keystoreIntegrity,omitempty" enum:"healthy,missing,corrupt" example:"healthy"`
	KeystoreCheckedAt    *int64          `json:"keystoreCheckedAt,omitempty" example:"1719662400000"`
	JournalState         *string         `json:"journalState,omitempty" enum:"healthy,degraded,paused,critical" doc:"Observed gateway event-journal pressure from the last control-mode heartbeat. degraded/paused/critical report durable-handoff disk pressure; null means the gateway has not reported telemetry." example:"healthy"`
	JournalEntries       *uint64         `json:"journalEntries,omitempty" doc:"Pending event-journal entries awaiting API acknowledgement, from the same observation as journalState." example:"42"`
	JournalBytes         *uint64         `json:"journalBytes,omitempty" doc:"Pending event-journal bytes awaiting API acknowledgement, from the same observation as journalState." example:"1048576"`
	EnrolledAt           *int64          `json:"enrolledAt,omitempty" example:"1719662400000"`
	ConnectedAt          *int64          `json:"connectedAt,omitempty" example:"1719662400000"`
	LastSeenAt           *int64          `json:"lastSeenAt,omitempty" example:"1719662400000"`
	CreatedAt            int64           `json:"createdAt" example:"1719662400000"`
	UpdatedAt            int64           `json:"updatedAt" example:"1719662400000"`
}

type GatewayCertificateSummary struct {
	ID               string  `json:"id" example:"gcrt_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	AuthorityID      string  `json:"authorityId" example:"pki_int_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	SerialNumber     string  `json:"serialNumber" example:"4ae91c2f"`
	Fingerprint      []byte  `json:"fingerprint" doc:"SHA-256 certificate fingerprint, base64 encoded by JSON."`
	NotBefore        int64   `json:"notBefore" example:"1719662400000"`
	NotAfter         int64   `json:"notAfter" example:"1719748800000"`
	CreatedAt        int64   `json:"createdAt" example:"1719662400000"`
	RevokedAt        *int64  `json:"revokedAt,omitempty"`
	RevocationReason *string `json:"revocationReason,omitempty"`
}

type GatewayAuditEntry struct {
	ID        string         `json:"id" example:"aud_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	ActorType string         `json:"actorType" enum:"user,system" example:"user"`
	ActorID   *string        `json:"actorId,omitempty" example:"user_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	Action    string         `json:"action" example:"gateway.drained"`
	Outcome   string         `json:"outcome" example:"success"`
	RequestID *string        `json:"requestId,omitempty" example:"req_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	Metadata  map[string]any `json:"metadata,omitempty" additionalProperties:"true"`
	CreatedAt int64          `json:"createdAt" example:"1719662400000"`
}

type GatewayAdminDetail struct {
	Gateway               GatewayAdmin                  `json:"gateway"`
	AssignedSessionCount  int                           `json:"assignedSessionCount" example:"2"`
	AssignedSessions      []domain.WASession            `json:"assignedSessions"`
	ActiveCertificate     *GatewayCertificateSummary    `json:"activeCertificate,omitempty"`
	Certificates          []GatewayCertificateSummary   `json:"certificates"`
	ReconciliationResults []GatewayReconciliationResult `json:"reconciliationResults"`
	Audit                 []GatewayAuditEntry           `json:"audit"`
}

// GatewayReconciliationResult is a non-secret per-device outcome from the
// current desired-state report. Unexpected local devices deliberately have no
// session ID or assignment epoch.
type GatewayReconciliationResult struct {
	DeviceJID       string  `json:"deviceJid" example:"6281234567890@s.whatsapp.net"`
	SessionID       *string `json:"sessionId,omitempty" example:"ses_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	AssignmentEpoch uint64  `json:"assignmentEpoch" example:"4"`
	Status          string  `json:"status" enum:"applied,keystore_missing,keystore_corrupt,unexpected_local_device" example:"applied"`
	DesiredRevision uint64  `json:"desiredRevision" example:"5"`
	UpdatedAt       int64   `json:"updatedAt" example:"1719662400000"`
}

// GatewayEnrollmentResult is returned only when an operator creates a gateway,
// replaces its unredeemed enrollment token, or explicitly re-enrolls it. Token
// is plaintext and deliberately absent from every read model.
type GatewayEnrollmentResult struct {
	GatewayID string `json:"gatewayId" example:"gw_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	TokenID   string `json:"tokenId" example:"ent_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	Token     string `json:"token" doc:"Single-use enrollment bearer. Copy it now: it is never returned by read APIs or audit data." example:"qwg_enroll_v1_example"`
	ExpiresAt int64  `json:"expiresAt" example:"1719663300000"`
}

func GatewayAdminFromDomain(g domain.Gateway) GatewayAdmin {
	return GatewayAdmin{
		ID: g.ID, Label: g.Label, Notes: g.Notes, Status: string(g.Status), SessionCount: g.SessionCount,
		Capacity: g.Capacity, BaseURL: g.BaseURL, GRPCEndpoint: g.GRPCEndpoint, SoftwareVersion: g.SoftwareVersion,
		Capabilities: append(json.RawMessage(nil), g.Capabilities...), ConnectionEpoch: g.ConnectionEpoch,
		ConnectionMode: g.ConnectionMode, DesiredLifecycle: g.DesiredLifecycle, DesiredRevision: g.DesiredRevision,
		AppliedRevision: g.AppliedRevision, EnrolledAt: g.EnrolledAt, ConnectedAt: g.ConnectedAt, LastSeenAt: g.LastSeenAt,
		ReconciliationStatus: g.ReconciliationStatus, KeystorePresent: g.KeystorePresent, KeystoreBytes: g.KeystoreBytes,
		KeystoreIntegrity: g.KeystoreIntegrity, KeystoreCheckedAt: g.KeystoreCheckedAt,
		JournalState: g.JournalState, JournalEntries: g.JournalEntries, JournalBytes: g.JournalBytes,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}
}

func GatewayAdminDetailFromDomain(d domain.GatewayAdminDetail) GatewayAdminDetail {
	out := GatewayAdminDetail{
		Gateway:               GatewayAdminFromDomain(d.Gateway),
		AssignedSessionCount:  d.AssignedSessionCount,
		AssignedSessions:      d.AssignedSessions,
		Certificates:          make([]GatewayCertificateSummary, 0, len(d.Certificates)),
		Audit:                 make([]GatewayAuditEntry, 0, len(d.Audit)),
		ReconciliationResults: make([]GatewayReconciliationResult, 0, len(d.ReconciliationResults)),
	}
	if out.AssignedSessions == nil {
		out.AssignedSessions = []domain.WASession{}
	}
	for _, c := range d.Certificates {
		mapped := GatewayCertificateSummary{
			ID:               c.ID,
			AuthorityID:      c.AuthorityID,
			SerialNumber:     c.SerialNumber,
			Fingerprint:      append([]byte(nil), c.Fingerprint...),
			NotBefore:        c.NotBefore,
			NotAfter:         c.NotAfter,
			CreatedAt:        c.CreatedAt,
			RevokedAt:        c.RevokedAt,
			RevocationReason: c.RevocationReason,
		}
		out.Certificates = append(out.Certificates, mapped)
		if d.ActiveCertificate != nil && c.ID == d.ActiveCertificate.ID {
			copy := mapped
			out.ActiveCertificate = &copy
		}
	}
	for _, a := range d.Audit {
		out.Audit = append(out.Audit, GatewayAuditEntry{
			ID:        a.ID,
			ActorType: a.ActorType,
			ActorID:   a.ActorID,
			Action:    a.Action,
			Outcome:   a.Outcome,
			RequestID: a.RequestID,
			Metadata:  a.Metadata,
			CreatedAt: a.CreatedAt,
		})
	}
	for _, r := range d.ReconciliationResults {
		out.ReconciliationResults = append(out.ReconciliationResults, GatewayReconciliationResult{
			DeviceJID:       r.DeviceJID,
			SessionID:       r.SessionID,
			AssignmentEpoch: r.AssignmentEpoch,
			Status:          r.Status,
			DesiredRevision: r.DesiredRevision,
			UpdatedAt:       r.UpdatedAt,
		})
	}
	return out
}
