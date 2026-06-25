package domain

// Sentinel error codes for the §11 API error envelope. Stable, machine-readable
// strings clients can switch on; they map to HTTP statuses at the transport edge.
const (
	CodeRateLimited     = "rate_limited"
	CodeNotFound        = "not_found"
	CodeUnauthorized    = "unauthorized"
	CodeForbidden       = "forbidden"
	CodeValidationError = "validation_error"
	CodeConflict        = "conflict"
	CodeNotImplemented  = "not_implemented"
	CodeInternal        = "internal"
)

// APIError is the §11 error envelope. It implements error so it can flow through
// normal Go error handling and be type-asserted at the HTTP boundary, where it
// serializes as: { "error": { "code", "message", "details" } } (see ErrorBody).
type APIError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Error implements the error interface.
func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// WithDetails attaches a details map and returns the same error for chaining.
func (e *APIError) WithDetails(details map[string]any) *APIError {
	e.Details = details
	return e
}

// ErrorBody is the JSON wrapper the §11 spec puts around an APIError:
// { "error": { … } }. Encode this at the HTTP edge.
type ErrorBody struct {
	Error *APIError `json:"error"`
}

// NewAPIError builds an APIError with the given code and message.
func NewAPIError(code, message string) *APIError {
	return &APIError{Code: code, Message: message}
}

// Convenience constructors for the common cases.
func ErrRateLimited(msg string) *APIError    { return NewAPIError(CodeRateLimited, msg) }
func ErrNotFound(msg string) *APIError       { return NewAPIError(CodeNotFound, msg) }
func ErrUnauthorized(msg string) *APIError   { return NewAPIError(CodeUnauthorized, msg) }
func ErrForbidden(msg string) *APIError      { return NewAPIError(CodeForbidden, msg) }
func ErrValidation(msg string) *APIError     { return NewAPIError(CodeValidationError, msg) }
func ErrConflict(msg string) *APIError       { return NewAPIError(CodeConflict, msg) }
func ErrNotImplemented(msg string) *APIError { return NewAPIError(CodeNotImplemented, msg) }
func ErrInternal(msg string) *APIError       { return NewAPIError(CodeInternal, msg) }
