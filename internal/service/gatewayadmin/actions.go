package gatewayadmin

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/enrollmenttoken"
	coreservice "github.com/ramaadi/quick-whatsapp-gateway/internal/service"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// StateConflictError is safe to expose as a conflict response. It intentionally
// does not reveal whether the gateway is missing or merely ineligible.
type StateConflictError struct{ Cause error }

func (e *StateConflictError) Error() string { return "gateway administration state conflict" }
func (e *StateConflictError) Unwrap() error { return e.Cause }

type Actor struct {
	UserID, RequestID string
}

type CreateGatewayInput struct {
	Label, Notes *string
	Capacity     *int
	Actor        Actor
}

// IssuedEnrollment is the only gatewayadmin DTO that carries a plaintext
// bearer. It is produced by the issue call and is never accepted by read or
// lifecycle APIs.
type IssuedEnrollment struct {
	GatewayID, TokenID, Token string
	ExpiresAt                 int64
}

type EnrollmentIssuer interface {
	CreateGateway(context.Context, coreservice.CreateGatewayInput) (coreservice.IssuedEnrollment, error)
	ReplaceTokenAs(context.Context, string, coreservice.Actor) (coreservice.IssuedEnrollment, error)
}

type GatewayAdminMutator interface {
	Drain(context.Context, string, int64, store.GatewayAdminAudit) error
	Resume(context.Context, string, int64, store.GatewayAdminAudit) error
	Disable(context.Context, string, int64, store.GatewayAdminAudit) error
	Reenable(context.Context, string, int64, store.GatewayAdminAudit) error
	Reenroll(context.Context, string, domain.EnrollmentToken, int64, []store.GatewayAdminAudit) error
	Delete(context.Context, string, bool, int64, store.GatewayAdminAudit) error
}

type ActionDependencies struct {
	Clock   func() time.Time
	Entropy io.Reader
	IDs     func() string
}

func defaultActionDependencies() ActionDependencies {
	return ActionDependencies{
		Clock:   time.Now,
		Entropy: rand.Reader,
		IDs:     func() string { return ulid.Make().String() },
	}
}

// NewWithActions augments the read service with the operator-only write use
// cases. The enrollment issuer remains the sole creation/replacement authority.
func NewWithActions(
	gateways GatewayReader,
	sessions SessionReader,
	audit AuditReader,
	enrollment EnrollmentIssuer,
	mutations GatewayAdminMutator,
) (*Service, error) {
	return NewWithActionDependencies(gateways, sessions, audit, enrollment, mutations, defaultActionDependencies())
}

func NewWithActionDependencies(
	gateways GatewayReader,
	sessions SessionReader,
	audit AuditReader,
	enrollment EnrollmentIssuer,
	mutations GatewayAdminMutator,
	deps ActionDependencies,
) (*Service, error) {
	if enrollment == nil || mutations == nil || deps.Clock == nil || deps.Entropy == nil || deps.IDs == nil {
		return nil, errors.New("gateway admin action dependencies required")
	}
	s := New(gateways, sessions, audit)
	s.enrollment, s.mutations, s.now, s.entropy, s.id = enrollment, mutations, deps.Clock, deps.Entropy, deps.IDs
	return s, nil
}

func (s *Service) CreateGateway(ctx context.Context, in CreateGatewayInput) (IssuedEnrollment, error) {
	if !validActor(in.Actor) {
		return IssuedEnrollment{}, ErrDenied
	}
	issued, err := s.enrollment.CreateGateway(ctx, coreservice.CreateGatewayInput{
		Label:           in.Label,
		Notes:           in.Notes,
		Capacity:        in.Capacity,
		CreatedByUserID: in.Actor.UserID,
		Actor: coreservice.Actor{
			Type:      "user",
			ID:        in.Actor.UserID,
			RequestID: in.Actor.RequestID,
		},
	})
	return fromIssued(issued), actionError(err)
}

func (s *Service) ReplaceEnrollmentToken(ctx context.Context, gatewayID string, actor Actor) (IssuedEnrollment, error) {
	if gatewayID == "" || !validActor(actor) {
		return IssuedEnrollment{}, ErrDenied
	}
	actorID := coreservice.Actor{Type: "user", ID: actor.UserID, RequestID: actor.RequestID}
	issued, err := s.enrollment.ReplaceTokenAs(ctx, gatewayID, actorID)
	return fromIssued(issued), actionError(err)
}

func (s *Service) Drain(ctx context.Context, gatewayID string, actor Actor) error {
	return s.mutate(ctx, gatewayID, actor, "gateway.drained", func(at int64, audit store.GatewayAdminAudit) error {
		return s.mutations.Drain(ctx, gatewayID, at, audit)
	})
}
func (s *Service) Resume(ctx context.Context, gatewayID string, actor Actor) error {
	return s.mutate(ctx, gatewayID, actor, "gateway.resumed", func(at int64, audit store.GatewayAdminAudit) error {
		return s.mutations.Resume(ctx, gatewayID, at, audit)
	})
}
func (s *Service) Disable(ctx context.Context, gatewayID string, actor Actor) error {
	return s.mutate(ctx, gatewayID, actor, "gateway.disabled", func(at int64, audit store.GatewayAdminAudit) error {
		return s.mutations.Disable(ctx, gatewayID, at, audit)
	})
}
func (s *Service) Reenable(ctx context.Context, gatewayID string, actor Actor) error {
	return s.mutate(ctx, gatewayID, actor, "gateway.reenabled", func(at int64, audit store.GatewayAdminAudit) error {
		return s.mutations.Reenable(ctx, gatewayID, at, audit)
	})
}

func (s *Service) Reenroll(ctx context.Context, gatewayID string, actor Actor) (IssuedEnrollment, error) {
	if gatewayID == "" || !validActor(actor) {
		return IssuedEnrollment{}, ErrDenied
	}
	now := s.now().UTC()
	token, persisted, err := s.newToken(now, gatewayID, actor.UserID)
	if err != nil {
		return IssuedEnrollment{}, err
	}
	audits := []store.GatewayAdminAudit{
		{
			Event: s.auditEvent(
				actor,
				"gateway.reenrolled",
				gatewayID,
				now.UnixMilli(),
				map[string]any{"enrollment_id": persisted.ID},
			),
		},
		{
			Event: s.auditEvent(
				actor,
				"enrollment.issued",
				gatewayID,
				now.UnixMilli(),
				map[string]any{"enrollment_id": persisted.ID},
			),
		},
	}
	if err := actionError(s.mutations.Reenroll(ctx, gatewayID, persisted, now.UnixMilli(), audits)); err != nil {
		return IssuedEnrollment{}, err
	}
	return IssuedEnrollment{
		GatewayID: gatewayID,
		TokenID:   token.Selector(),
		Token:     token.String(),
		ExpiresAt: persisted.ExpiresAt,
	}, nil
}

func (s *Service) Delete(ctx context.Context, gatewayID string, consequencesAcknowledged bool, actor Actor) error {
	if !consequencesAcknowledged {
		return &StateConflictError{Cause: store.ErrGatewayAdminState}
	}
	return s.mutate(ctx, gatewayID, actor, "gateway.deleted", func(at int64, audit store.GatewayAdminAudit) error {
		return s.mutations.Delete(ctx, gatewayID, true, at, audit)
	})
}

var ErrDenied = errors.New("gateway administration denied")

func (s *Service) mutate(
	ctx context.Context,
	gatewayID string,
	actor Actor,
	action string,
	fn func(int64, store.GatewayAdminAudit) error,
) error {
	if gatewayID == "" || !validActor(actor) {
		return ErrDenied
	}
	now := s.now().UTC().UnixMilli()
	return actionError(fn(now, store.GatewayAdminAudit{Event: s.auditEvent(actor, action, gatewayID, now, nil)}))
}

func validActor(actor Actor) bool { return actor.UserID != "" }

func (s *Service) newToken(
	now time.Time,
	gatewayID, userID string,
) (enrollmenttoken.Token, domain.EnrollmentToken, error) {
	token, err := enrollmenttoken.GenerateWith(s.entropy, s.id)
	if err != nil {
		return enrollmenttoken.Token{}, domain.EnrollmentToken{}, err
	}
	digest := enrollmenttoken.Digest(token.String())
	config := coreservice.DefaultEnrollmentConfig()
	return token, domain.EnrollmentToken{
		ID:              token.Selector(),
		GatewayID:       gatewayID,
		TokenHash:       digest[:],
		TokenPrefix:     safePrefix(token.String()),
		MaxAttempts:     config.MaxAttempts,
		ExpiresAt:       now.Add(config.TokenTTL).UnixMilli(),
		CreatedByUserID: userID,
		CreatedAt:       now.UnixMilli(),
		UpdatedAt:       now.UnixMilli(),
	}, nil
}

func (s *Service) auditEvent(
	actor Actor,
	action, gatewayID string,
	at int64,
	metadata map[string]any,
) domain.AuditEvent {
	actorID, requestID := actor.UserID, actor.RequestID
	var request *string
	if requestID != "" {
		request = &requestID
	}
	return domain.AuditEvent{
		ID:           "aud_" + s.id(),
		ActorType:    "user",
		ActorID:      &actorID,
		Action:       action,
		ResourceType: "gateway",
		ResourceID:   &gatewayID,
		Outcome:      "success",
		RequestID:    request,
		Metadata:     metadata,
		CreatedAt:    at,
	}
}

func fromIssued(value coreservice.IssuedEnrollment) IssuedEnrollment {
	return IssuedEnrollment{
		GatewayID: value.GatewayID,
		TokenID:   value.TokenID,
		Token:     value.Token,
		ExpiresAt: value.ExpiresAt,
	}
}
func actionError(err error) error {
	if errors.Is(err, store.ErrGatewayAdminState) || errors.Is(err, store.ErrEnrollmentState) {
		return &StateConflictError{Cause: err}
	}
	return err
}

func safePrefix(token string) string {
	if len(token) > 16 {
		return token[:16]
	}
	return token
}
