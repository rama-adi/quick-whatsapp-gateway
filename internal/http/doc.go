// Package httpx builds the gateway's chi router: the JSON API under /api/v1
// behind the two-acceptor auth middleware (JWKS-verified JWT or better-auth
// api-key) plus the capability gates, and unauthenticated health/readiness/
// metrics probes. The gateway is a pure WhatsApp engine — it has no human login
// surface and serves no frontend assets.
package httpx
