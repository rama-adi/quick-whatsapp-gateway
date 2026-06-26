// Package middleware holds the gateway's transport-level HTTP middleware: rate
// limiting, panic recovery, request-id propagation, and request logging. Caller
// authentication (JWT/api-key) and authorization gates live in internal/authz.
package middleware
