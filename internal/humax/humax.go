// Package humax adapts huma to this gateway's conventions so the REST API can be
// declared code-first (Go input/output structs are the source of truth for the
// generated OpenAPI spec, D11) while preserving the exact v2 wire contract:
//
//   - the §11 error envelope {"error":{"code","message","details"}} (NOT huma's
//     default RFC7807 {type,title,status,detail}) — installed by overriding
//     huma.NewError, so both handler-returned errors and huma's own request
//     validation render the envelope;
//   - the capability gates (RequireRead/Send/Manage/Events/SuperAdmin) as huma
//     middleware reading the assertion-resolved authz.Principal off the context;
//   - the organization id lifted from that principal for org-scoped service calls.
package humax

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func init() {
	// Render every huma-originated error (request validation, 404s, the errors our
	// handlers return) as the §11 envelope. huma calls NewError for validation
	// failures; it also marshals a returned StatusError directly — apiError
	// satisfies both paths.
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		// huma uses 422 for request validation; the v2 contract uses 400
		// validation_error, so coerce it.
		if status == http.StatusUnprocessableEntity {
			status = http.StatusBadRequest
		}
		ae := domain.NewAPIError(codeForStatus(status), msg)
		if len(errs) > 0 {
			details := make([]string, 0, len(errs))
			for _, e := range errs {
				if e != nil {
					details = append(details, e.Error())
				}
			}
			if len(details) > 0 {
				ae = ae.WithDetails(map[string]any{"errors": details})
			}
		}
		return &apiError{err: ae}
	}
}

// apiError wraps a *domain.APIError so huma treats it as a StatusError and
// marshals it as the §11 envelope.
type apiError struct{ err *domain.APIError }

func (e *apiError) Error() string  { return e.err.Error() }
func (e *apiError) GetStatus() int { return statusForCode(e.err.Code) }
func (e *apiError) MarshalJSON() ([]byte, error) {
	return json.Marshal(domain.ErrorBody{Error: e.err})
}

// Err maps any error into the huma error envelope: a *domain.APIError keeps its
// code/status; anything else is masked as a generic 500 so internals never leak.
// Handlers return Err(serviceErr) so the wire shape matches the old WriteError.
func Err(err error) error {
	var ae *domain.APIError
	if errors.As(err, &ae) {
		return &apiError{err: ae}
	}
	return &apiError{err: domain.ErrInternal("internal server error")}
}

// NewAPI builds a huma API mounted on the given chi router with this gateway's
// error model and no built-in docs/schema routes (the router serves the spec).
func NewAPI(r chi.Router) huma.API {
	return humachi.New(r, config())
}

// NewSpecAPI builds a huma API over a throwaway router for cmd/genopenapi: it only
// needs the operation declarations to emit the spec, never serves traffic.
func NewSpecAPI() huma.API {
	return humachi.New(chi.NewRouter(), config())
}

// config is the shared huma config: no built-in docs/schema routes (the router
// serves docs/openapi.yaml itself) and NO transformers — the default
// schema-link transformer injects a `$schema` field into every response body,
// which would pollute the v2 wire contract (and clobber our error envelope).
func config() huma.Config {
	cfg := huma.DefaultConfig("WhatsApp Gateway API", "v1")
	cfg.OpenAPIPath = ""
	cfg.DocsPath = ""
	cfg.SchemasPath = ""
	// The schema-link transformer is injected via a CreateHook (runs at API
	// construction), so clearing Transformers alone is not enough — drop the hooks
	// too. Without this every response body (and our error envelope) gets a
	// `$schema` field that is not part of the v2 wire contract.
	cfg.Transformers = nil
	cfg.CreateHooks = nil
	return cfg
}

// RequireCap returns a huma operation middleware enforcing a capability gate on
// the assertion-resolved principal — the huma-native equivalent of authz.Require.
func RequireCap(api huma.API, c authz.Capability) func(huma.Context, func(huma.Context)) {
	return func(hctx huma.Context, next func(huma.Context)) {
		p := authz.FromContext(hctx.Context())
		if p == nil {
			_ = huma.WriteErr(api, hctx, http.StatusUnauthorized, "authentication required")
			return
		}
		if !authz.Allow(p, c) {
			_ = huma.WriteErr(api, hctx, http.StatusForbidden, "missing required capability: "+string(c))
			return
		}
		next(hctx)
	}
}

// RequireSuperAdmin gates an operation on the platform super_admin role.
func RequireSuperAdmin(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(hctx huma.Context, next func(huma.Context)) {
		p := authz.FromContext(hctx.Context())
		if p == nil {
			_ = huma.WriteErr(api, hctx, http.StatusUnauthorized, "authentication required")
			return
		}
		if !p.IsSuperAdmin() {
			_ = huma.WriteErr(api, hctx, http.StatusForbidden, "super_admin required")
			return
		}
		next(hctx)
	}
}

// Org returns the caller's organization id from the assertion-resolved principal,
// or an unauthorized error if none is present. Org-scoped service calls use it.
func Org(ctx context.Context) (string, error) {
	p := authz.FromContext(ctx)
	if p == nil || p.OrganizationID == "" {
		return "", Err(domain.ErrUnauthorized("authentication required"))
	}
	return p.OrganizationID, nil
}

// Principal returns the resolved caller, or an unauthorized error.
func Principal(ctx context.Context) (*authz.Principal, error) {
	p := authz.FromContext(ctx)
	if p == nil {
		return nil, Err(domain.ErrUnauthorized("authentication required"))
	}
	return p, nil
}

// statusForCode / codeForStatus translate between the §11 error codes and HTTP
// statuses (kept here so the huma error model is self-contained).
func statusForCode(code string) int {
	switch code {
	case domain.CodeNotFound:
		return http.StatusNotFound
	case domain.CodeUnauthorized:
		return http.StatusUnauthorized
	case domain.CodeForbidden:
		return http.StatusForbidden
	case domain.CodeValidationError:
		return http.StatusBadRequest
	case domain.CodeRateLimited:
		return http.StatusTooManyRequests
	case domain.CodeConflict:
		return http.StatusConflict
	case domain.CodeNotImplemented:
		return http.StatusNotImplemented
	case domain.CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusNotFound:
		return domain.CodeNotFound
	case http.StatusUnauthorized:
		return domain.CodeUnauthorized
	case http.StatusForbidden:
		return domain.CodeForbidden
	case http.StatusBadRequest:
		return domain.CodeValidationError
	case http.StatusTooManyRequests:
		return domain.CodeRateLimited
	case http.StatusConflict:
		return domain.CodeConflict
	case http.StatusNotImplemented:
		return domain.CodeNotImplemented
	case http.StatusServiceUnavailable:
		return domain.CodeUnavailable
	default:
		return domain.CodeInternal
	}
}
