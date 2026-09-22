package gatewayadmin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	coreservice "github.com/rama-adi/quick-whatsapp-gateway/internal/service"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

type enrollmentIssuerStub struct {
	created, replaced coreservice.IssuedEnrollment
	err               error
}

func (s enrollmentIssuerStub) CreateGateway(context.Context, coreservice.CreateGatewayInput) (coreservice.IssuedEnrollment, error) {
	return s.created, s.err
}
func (s enrollmentIssuerStub) ReplaceTokenAs(context.Context, string, coreservice.Actor) (coreservice.IssuedEnrollment, error) {
	return s.replaced, s.err
}

type mutationsStub struct {
	action string
	token  domain.EnrollmentToken
	audits []store.GatewayAdminAudit
	err    error
}

func (s *mutationsStub) Drain(context.Context, string, int64, store.GatewayAdminAudit) error {
	s.action = "drain"
	return s.err
}
func (s *mutationsStub) Resume(context.Context, string, int64, store.GatewayAdminAudit) error {
	s.action = "resume"
	return s.err
}
func (s *mutationsStub) Disable(context.Context, string, int64, store.GatewayAdminAudit) error {
	s.action = "disable"
	return s.err
}
func (s *mutationsStub) Reenable(context.Context, string, int64, store.GatewayAdminAudit) error {
	s.action = "reenable"
	return s.err
}
func (s *mutationsStub) Reenroll(_ context.Context, _ string, token domain.EnrollmentToken, _ int64, audits []store.GatewayAdminAudit) error {
	s.action, s.token, s.audits = "reenroll", token, audits
	return s.err
}
func (s *mutationsStub) Delete(context.Context, string, bool, int64, store.GatewayAdminAudit) error {
	s.action = "delete"
	return s.err
}

func actionService(t *testing.T, issuer EnrollmentIssuer, mutations GatewayAdminMutator) *Service {
	t.Helper()
	s, err := NewWithActionDependencies(gatewayStub{}, sessionStub{}, auditStub{}, issuer, mutations, ActionDependencies{
		Clock:   func() time.Time { return time.Unix(100, 0) },
		Entropy: strings.NewReader(strings.Repeat("x", 128)),
		IDs:     func() string { return "01ARZ3NDEKTSV4RRFFQ69G5FAV" },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReenrollIssuesBearerOnlyAfterMutationAndAuditsNoSecret(t *testing.T) {
	mutations := &mutationsStub{}
	s := actionService(t, enrollmentIssuerStub{}, mutations)
	issued, err := s.Reenroll(context.Background(), "gw_1", Actor{UserID: "user_1", RequestID: "req_1"})
	if err != nil {
		t.Fatal(err)
	}
	if mutations.action != "reenroll" || issued.Token == "" || issued.TokenID != mutations.token.ID || issued.ExpiresAt != mutations.token.ExpiresAt {
		t.Fatalf("issued=%+v mutation=%+v", issued, mutations)
	}
	if len(mutations.audits) != 2 || mutations.audits[0].Event.Action != "gateway.reenrolled" || mutations.audits[1].Event.Action != "enrollment.issued" {
		t.Fatalf("audits=%+v", mutations.audits)
	}
	for _, event := range mutations.audits {
		if event.Event.RequestID == nil || *event.Event.RequestID != "req_1" || strings.Contains(strings.ToLower(fmt.Sprint(event.Event.Metadata)), strings.ToLower(issued.Token)) {
			t.Fatalf("unsafe or incomplete audit: %+v", event.Event)
		}
	}
}

func TestActionStateConflictsArePublicAndActorIsRequired(t *testing.T) {
	mutations := &mutationsStub{err: store.ErrGatewayAdminState}
	s := actionService(t, enrollmentIssuerStub{}, mutations)
	if err := s.Disable(context.Background(), "gw_1", Actor{UserID: "user_1"}); err == nil {
		t.Fatal("accepted state conflict")
	} else {
		var conflict *StateConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("error=%T %v", err, err)
		}
	}
	mutations.action = ""
	if err := s.Disable(context.Background(), "gw_1", Actor{}); !errors.Is(err, ErrDenied) || mutations.action != "" {
		t.Fatalf("error=%v action=%q", err, mutations.action)
	}
}

func TestDeleteRequiresExplicitAcknowledgement(t *testing.T) {
	mutations := &mutationsStub{}
	s := actionService(t, enrollmentIssuerStub{}, mutations)
	err := s.Delete(context.Background(), "gw_1", false, Actor{UserID: "user_1"})
	var conflict *StateConflictError
	if !errors.As(err, &conflict) || mutations.action != "" {
		t.Fatalf("error=%v action=%q", err, mutations.action)
	}
}
