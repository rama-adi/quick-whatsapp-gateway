// Package httpx hosts the shared HTTP handler building blocks: the huma
// operations (handlers/) that the API mounts on its public REST surface, plus
// reusable middleware. The gateway binary serves no HTTP API — it exposes only
// the private mTLS engine gRPC listener and minimal net/http probes.
package httpx
