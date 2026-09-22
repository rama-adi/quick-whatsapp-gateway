package apigrpc

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
)

// Metadata credential names, mirroring the HTTP headers the two-acceptor
// policy reads: "authorization: Bearer <jwt>" and "x-api-key: <key>".
// gRPC metadata keys are lowercased; the incoming-metadata map is case
// insensitive on lookup.
const (
	metadataAuthorization = "authorization"
	metadataAPIKey        = "x-api-key"
	bearerPrefix          = "bearer "
)

// Interceptors builds the unary + stream authentication interceptors for the
// public server. Both resolve the caller through authz.ResolveCredential — the
// exact code path the HTTP middleware runs — and stash the verified Principal
// (and its organization id) on the RPC context via authz.SetPrincipal, so
// capability checks (authz.Allow) and org scoping behave identically to REST.
// Missing or invalid credentials are rejected with Unauthenticated.
func Interceptors(
	tokens authz.TokenVerifier,
	keys authz.KeyVerifier,
) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	unary := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isPublicMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		authed, err := authenticate(ctx, tokens, keys)
		if err != nil {
			return nil, err
		}
		return handler(authed, req)
	}
	stream := func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isPublicMethod(info.FullMethod) {
			return handler(srv, stream)
		}
		authed, err := authenticate(stream.Context(), tokens, keys)
		if err != nil {
			return err
		}
		return handler(srv, &authorizedStream{ServerStream: stream, ctx: authed})
	}
	return unary, stream
}

// isPublicMethod exempts liveness probes from credential checks, mirroring the
// unauthenticated /healthz and /readyz HTTP routes.
func isPublicMethod(fullMethod string) bool {
	return fullMethod == "/public.v1.PublicHealthService/Check"
}

// authenticate resolves the two-acceptor credential from gRPC metadata. Every
// intercepted call requires a valid principal except the health-check
// exemption in isPublicMethod.
func authenticate(ctx context.Context, tokens authz.TokenVerifier, keys authz.KeyVerifier) (context.Context, error) {
	bearer, hasBearer := bearerFromMetadata(ctx)
	p := authz.ResolveCredential(
		ctx,
		tokens,
		keys,
		bearer,
		hasBearer,
		apiKeyFromMetadata(ctx),
	)
	if p == nil {
		return ctx, status.Error(codes.Unauthenticated, "missing or invalid credentials")
	}
	return authz.SetPrincipal(ctx, p), nil
}

// bearerFromMetadata extracts "<token>" from "authorization: Bearer <token>"
// (scheme match case-insensitive, mirroring the HTTP header parse).
func bearerFromMetadata(ctx context.Context) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	values := md.Get(metadataAuthorization)
	if len(values) == 0 {
		return "", false
	}
	h := strings.TrimSpace(values[0])
	if len(h) <= len(bearerPrefix) || !strings.EqualFold(h[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(bearerPrefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}

func apiKeyFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get(metadataAPIKey)
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

// authorizedStream replaces the stream's context with the authenticated one so
// the handler sees the Principal. SendMsg/RecvMsg are forwarded explicitly so
// grpc-go's server-side bookkeeping keeps working through the wrapper.
type authorizedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authorizedStream) Context() context.Context { return s.ctx }

func (s *authorizedStream) SendMsg(m any) error { return s.ServerStream.SendMsg(m) }
func (s *authorizedStream) RecvMsg(m any) error { return s.ServerStream.RecvMsg(m) }
