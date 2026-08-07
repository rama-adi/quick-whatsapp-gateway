package domain

// EnrollmentToken contains only persisted, non-plaintext token material.
type EnrollmentToken struct {
	ID, GatewayID, TokenPrefix, Status, CreatedByUserID string
	TokenHash                                           []byte
	AttemptCount, MaxAttempts                           uint32
	ExpiresAt, CreatedAt, UpdatedAt                     int64
	RedemptionNonce, CSRSHA256                          []byte
	RedeemingAt, LeaseExpiresAt, ConsumedAt, RevokedAt  *int64
}

type GatewayControlMetadata struct {
	GatewayID                            string
	Notes, GRPCEndpoint, SoftwareVersion *string
	DesiredRevision, AppliedRevision     uint64
	Capabilities                         []byte
	EnrolledAt, ConnectedAt              *int64
}

// GatewayConnection identifies one accepted control-stream incarnation. Epochs
// increase for the lifetime of a gateway row and fence writes from stale streams.
type GatewayConnection struct {
	GatewayID       string
	ConnectionEpoch uint64
}

type GatewayAcceptedConnection struct {
	ConnectionEpoch  uint64
	DesiredLifecycle string
	DesiredRevision  uint64
}

type GatewayConnectionHello struct {
	GatewayID       string
	BaseURL         *string
	GRPCEndpoint    *string
	SoftwareVersion *string
	Capabilities    []byte
	SessionCount    int
	Status          GatewayStatus
}

type GatewayHeartbeat struct {
	GatewayConnection
	SessionCount int
	Status       GatewayStatus
}

type GatewayLifecycleReport struct {
	GatewayConnection
	Status GatewayStatus
}

// GatewayDesiredSession is one organization-scoped, fenced assignment in the
// API's complete desired-state snapshot.
type GatewayDesiredSession struct {
	SessionID, OrganizationID, DeviceJID string
	AssignmentEpoch, ConfigRevision      uint64
	AutoRead, PresenceTyping             bool
	DesiredRun                           bool
	RatePerMin, RatePerHour              uint32
	LeaseExpiresAt                       int64
}

// PKIAuthority carries encrypted private-key material only.
type PKIAuthority struct {
	ID, Kind, Status, CertificatePEM, EncryptionKeyID            string
	ParentAuthorityID                                            *string
	CertificateFingerprint, EncryptedPrivateKey, PrivateKeyNonce []byte
	NotBefore, NotAfter, CreatedAt, UpdatedAt                    int64
}

type GatewayCertificate struct {
	ID, GatewayID, AuthorityID, IssuanceKind, SerialNumber, CertificatePEM, TrustBundlePEM string
	EnrollmentTokenID                                                                      *string
	CSRSHA256                                                                              []byte
	Fingerprint                                                                            []byte
	NotBefore, NotAfter, CreatedAt                                                         int64
	RevokedAt                                                                              *int64
	RevocationReason                                                                       *string
}

type AuditEvent struct {
	ID, ActorType, Action, ResourceType, Outcome   string
	OrganizationID, ActorID, ResourceID, RequestID *string
	SourceIP                                       []byte
	Metadata                                       map[string]any
	CreatedAt                                      int64
}
