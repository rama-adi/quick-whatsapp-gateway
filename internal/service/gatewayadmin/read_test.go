package gatewayadmin

import (
	"context"
	"errors"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

type gatewayStub struct {
	list  []domain.Gateway
	one   domain.Gateway
	certs []domain.GatewayCertificateSummary
	err   error
}

func (s gatewayStub) List(context.Context) ([]domain.Gateway, error)      { return s.list, s.err }
func (s gatewayStub) Get(context.Context, string) (domain.Gateway, error) { return s.one, s.err }
func (s gatewayStub) ListCertificateSummaries(context.Context, string) ([]domain.GatewayCertificateSummary, error) {
	return s.certs, s.err
}

type sessionStub struct {
	rows []domain.WASession
	err  error
}

func (s sessionStub) ListByGateway(context.Context, string) ([]domain.WASession, error) {
	return s.rows, s.err
}

type auditStub struct {
	rows []domain.AuditEvent
	err  error
}

func (s auditStub) ListByResource(context.Context, string, string, int64, int) ([]domain.AuditEvent, error) {
	return s.rows, s.err
}

func TestGetGatewayBuildsSafeDerivedView(t *testing.T) {
	revoked := int64(90)
	secretMetadata := map[string]any{"gateway_id": "gw_1"}
	service := New(
		gatewayStub{one: domain.Gateway{ID: "gw_1", SessionCount: 99}, certs: []domain.GatewayCertificateSummary{
			{ID: "revoked", RevokedAt: &revoked}, {ID: "active", SerialNumber: "42"},
		}},
		sessionStub{rows: []domain.WASession{{ID: "s1"}, {ID: "s2"}}},
		auditStub{rows: []domain.AuditEvent{{
			ID: "a1", ActorType: "user", ActorID: ptr("u1"), Action: "gateway.created",
			Outcome: "success", RequestID: ptr("r1"), SourceIP: []byte("secret-ip"),
			Metadata: secretMetadata, CreatedAt: 100,
		}}},
	)
	got, err := service.GetGateway(context.Background(), "gw_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.AssignedSessionCount != 2 || got.SessionCount != 99 {
		t.Fatalf("reported/derived counts were not kept distinct: %+v", got)
	}
	if got.ActiveCertificate == nil || got.ActiveCertificate.ID != "active" {
		t.Fatalf("active certificate = %+v", got.ActiveCertificate)
	}
	if len(got.Audit) != 1 || got.Audit[0].ID != "a1" {
		t.Fatalf("audit projection = %+v", got.Audit)
	}
}

func TestReadErrorsRetainCause(t *testing.T) {
	want := errors.New("database unavailable")
	service := New(gatewayStub{err: want}, sessionStub{}, auditStub{})
	if _, err := service.ListGateways(context.Background()); !errors.Is(err, want) {
		t.Fatalf("ListGateways error = %v", err)
	}
	if _, err := service.GetGateway(context.Background(), "gw"); !errors.Is(err, want) {
		t.Fatalf("GetGateway error = %v", err)
	}
}

func ptr(value string) *string { return &value }
