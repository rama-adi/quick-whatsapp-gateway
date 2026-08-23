package apigrpc

import (
	"google.golang.org/grpc"

	publicv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/public/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// Deps carries the application services the public gRPC surface delegates to.
// The composition root passes the same values it gives the REST handlers
// (handlers.Handlers fields), so both transports share one service graph.
type Deps struct {
	Tokens   authz.TokenVerifier
	Keys     authz.KeyVerifier
	Sessions SessionsDeps
	Messages MessagesDeps
	Chats    ChatsReader
	Events   EventsReader
	// StreamConfig bounds the events tail; zero values are sensible.
	StreamConfig StreamConfig
}

// RegisterServer builds a grpc.Server with the authn interceptors installed and
// every public.v1 service registered.
//
// NOTE: gRPC reflection is deliberately NOT enabled — the public surface is
// served from the committed public/v1 contracts only (policy: unsupported
// publicly; see docs/specs/grpc-contracts.md). The private gateway.v1 services
// must never be registered here either.
func RegisterServer(deps Deps) *grpc.Server {
	unary, stream := Interceptors(deps.Tokens, deps.Keys)
	server := grpc.NewServer(grpc.UnaryInterceptor(unary), grpc.StreamInterceptor(stream))
	publicv1.RegisterPublicSessionsServiceServer(server, NewSessions(deps.Sessions))
	publicv1.RegisterPublicMessagesServiceServer(server, NewMessages(deps.Messages, deps.Chats))
	publicv1.RegisterPublicEventsServiceServer(server, NewEvents(deps.Events, deps.StreamConfig))
	return server
}

// clampLimit reproduces the REST pagination clamp: 0 → default 50, bounded to
// [MinLimit, MaxLimit] — identical to handlers.clampLimit / httpx.ParsePage.
func clampLimit(limit int) int {
	if limit == 0 {
		return httpx.DefaultLimit
	}
	return min(max(limit, httpx.MinLimit), httpx.MaxLimit)
}
