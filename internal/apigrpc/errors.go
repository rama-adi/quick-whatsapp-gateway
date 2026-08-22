// Package apigrpc adapts the public gRPC surface (public.v1) onto the same
// application services the REST handlers use. It owns three concerns only:
//
//   - authentication: metadata-based JWT/api-key interceptors that resolve the
//     SAME authz.Principal as the HTTP middleware (authz.ResolveCredential);
//   - error mapping: one domain.APIError → gRPC status mapper shared by every
//     service implementation;
//   - the service implementations themselves, which are thin translations
//     between protobuf messages and service-layer calls — no business logic.
//
// The package defines small consumer interfaces mirroring exactly the service
// methods it uses, so tests run against fakes with no MySQL/Redis. Handlers
// must not invoke REST, and REST must not invoke gRPC; both adapt into the same
// application services (plan §5 dependency direction).
package apigrpc

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// Status maps any error into a gRPC status error, mirroring humax's
// domain-code → HTTP-status table:
//
//	not_found        → NotFound
//	unauthorized     → Unauthenticated
//	forbidden        → PermissionDenied
//	validation_error → InvalidArgument
//	conflict         → FailedPrecondition  (same code the private engine uses)
//	rate_limited     → ResourceExhausted
//	not_implemented  → Unimplemented
//	gateway_unavailable → Unavailable
//	internal / unknown → Internal
//
// context.DeadlineExceeded/Canceled keep their natural codes so client retries
// behave like the HTTP timeout path (which reports them as retryable 503).
func Status(err error) error {
	if err == nil {
		return nil
	}
	var ae *domain.APIError
	if errors.As(err, &ae) {
		return status.Error(codeForDomain(ae.Code), ae.Message)
	}
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "request timed out")
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}

func codeForDomain(code string) codes.Code {
	switch code {
	case domain.CodeNotFound:
		return codes.NotFound
	case domain.CodeUnauthorized:
		return codes.Unauthenticated
	case domain.CodeForbidden:
		return codes.PermissionDenied
	case domain.CodeValidationError:
		return codes.InvalidArgument
	case domain.CodeConflict:
		return codes.FailedPrecondition
	case domain.CodeRateLimited:
		return codes.ResourceExhausted
	case domain.CodeNotImplemented:
		return codes.Unimplemented
	case domain.CodeUnavailable:
		return codes.Unavailable
	default:
		return codes.Internal
	}
}
