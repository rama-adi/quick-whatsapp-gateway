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

// PKIAuthority carries encrypted private-key material only.
type PKIAuthority struct {
	ID, Kind, Status, CertificatePEM, EncryptionKeyID            string
	ParentAuthorityID                                            *string
	CertificateFingerprint, EncryptedPrivateKey, PrivateKeyNonce []byte
	NotBefore, NotAfter, CreatedAt, UpdatedAt                    int64
}

type GatewayCertificate struct {
	ID, GatewayID, AuthorityID, EnrollmentTokenID, SerialNumber, CertificatePEM string
	CSRSHA256                                                                   []byte
	Fingerprint                                                                 []byte
	NotBefore, NotAfter, CreatedAt                                              int64
	RevokedAt                                                                   *int64
	RevocationReason                                                            *string
}

type AuditEvent struct {
	ID, ActorType, Action, ResourceType, Outcome   string
	OrganizationID, ActorID, ResourceID, RequestID *string
	SourceIP                                       []byte
	Metadata                                       map[string]any
	CreatedAt                                      int64
}
