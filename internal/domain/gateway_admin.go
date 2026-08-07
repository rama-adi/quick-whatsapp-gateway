package domain

// GatewayCertificateSummary is the non-secret administrative projection of a
// gateway certificate. It deliberately cannot carry PEM, CSR, enrollment-token,
// or trust-bundle material.
type GatewayCertificateSummary struct {
	ID, AuthorityID, SerialNumber  string
	Fingerprint                    []byte
	NotBefore, NotAfter, CreatedAt int64
	RevokedAt                      *int64
	RevocationReason               *string
}

// GatewayAuditEntry is the safe operator-facing audit projection. Source IP and
// organization metadata remain in the persistence model but do not cross this
// service seam.
type GatewayAuditEntry struct {
	ID, ActorType, Action, Outcome string
	ActorID, RequestID             *string
	Metadata                       map[string]any
	CreatedAt                      int64
}

// GatewayAdminDetail is a transport-independent gateway administration view.
// AssignedSessionCount is derived from AssignedSessions rather than trusting the
// gateway's self-reported SessionCount.
type GatewayAdminDetail struct {
	Gateway
	AssignedSessionCount  int
	AssignedSessions      []WASession
	ActiveCertificate     *GatewayCertificateSummary
	Certificates          []GatewayCertificateSummary
	ReconciliationResults []GatewayReconciliationResult
	Audit                 []GatewayAuditEntry
}
