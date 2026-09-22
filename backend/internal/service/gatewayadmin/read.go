// Package gatewayadmin provides transport-independent administrative gateway
// use cases. Its DTOs are deliberately safe for presentation by an authenticated
// operator API.
package gatewayadmin

import (
	"context"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

const auditPageSize = 100

type GatewayReader interface {
	List(context.Context) ([]domain.Gateway, error)
	Get(context.Context, string) (domain.Gateway, error)
	ListCertificateSummaries(context.Context, string) ([]domain.GatewayCertificateSummary, error)
	ListReconciliationResults(context.Context, string) ([]domain.GatewayReconciliationResult, error)
}

type SessionReader interface {
	ListByGateway(context.Context, string) ([]domain.WASession, error)
}

type AuditReader interface {
	ListByResource(context.Context, string, string, int64, int) ([]domain.AuditEvent, error)
}

type Service struct {
	gateways GatewayReader
	sessions SessionReader
	audit    AuditReader

	enrollment EnrollmentIssuer
	mutations  GatewayAdminMutator
	now        func() time.Time
	entropy    io.Reader
	id         func() string
}

func New(gateways GatewayReader, sessions SessionReader, audit AuditReader) *Service {
	return &Service{gateways: gateways, sessions: sessions, audit: audit}
}

func (s *Service) ListGateways(ctx context.Context) ([]domain.Gateway, error) {
	gateways, err := s.gateways.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("gateway admin: list gateways: %w", err)
	}
	return gateways, nil
}

func (s *Service) GetGateway(ctx context.Context, gatewayID string) (domain.GatewayAdminDetail, error) {
	gateway, err := s.gateways.Get(ctx, gatewayID)
	if err != nil {
		return domain.GatewayAdminDetail{}, fmt.Errorf("gateway admin: get gateway: %w", err)
	}
	sessions, err := s.sessions.ListByGateway(ctx, gatewayID)
	if err != nil {
		return domain.GatewayAdminDetail{}, fmt.Errorf("gateway admin: list assigned sessions: %w", err)
	}
	certificates, err := s.gateways.ListCertificateSummaries(ctx, gatewayID)
	if err != nil {
		return domain.GatewayAdminDetail{}, fmt.Errorf("gateway admin: list certificate summaries: %w", err)
	}
	reconciliation, err := s.gateways.ListReconciliationResults(ctx, gatewayID)
	if err != nil {
		return domain.GatewayAdminDetail{}, fmt.Errorf("gateway admin: list reconciliation results: %w", err)
	}
	audit, err := s.audit.ListByResource(ctx, "gateway", gatewayID, math.MaxInt64, auditPageSize)
	if err != nil {
		return domain.GatewayAdminDetail{}, fmt.Errorf("gateway admin: list audit: %w", err)
	}
	detail := domain.GatewayAdminDetail{
		Gateway:               gateway,
		AssignedSessionCount:  len(sessions),
		AssignedSessions:      sessions,
		Certificates:          certificates,
		ReconciliationResults: reconciliation,
		Audit:                 make([]domain.GatewayAuditEntry, 0, len(audit)),
	}
	for i := range certificates {
		if certificates[i].RevokedAt == nil {
			detail.ActiveCertificate = &detail.Certificates[i]
			break
		}
	}
	for _, event := range audit {
		detail.Audit = append(detail.Audit, domain.GatewayAuditEntry{
			ID:        event.ID,
			ActorType: event.ActorType,
			ActorID:   event.ActorID,
			Action:    event.Action,
			Outcome:   event.Outcome,
			RequestID: event.RequestID,
			Metadata:  event.Metadata,
			CreatedAt: event.CreatedAt,
		})
	}
	return detail, nil
}
