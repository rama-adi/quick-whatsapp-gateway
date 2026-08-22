package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	publicv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/public/v1"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/apigrpc"
	"google.golang.org/grpc"
)

type readinessGate struct {
	dependencies func() error
	admitting    atomic.Bool
}

func (g *readinessGate) check() error {
	if !g.admitting.Load() {
		return errors.New("api is not accepting traffic")
	}
	if g.dependencies != nil {
		return g.dependencies()
	}
	return nil
}

type publicHealthService struct {
	publicv1.UnimplementedPublicHealthServiceServer
	readiness func() error
}

func (s publicHealthService) Check(context.Context, *publicv1.PublicHealthServiceCheckRequest) (*publicv1.PublicHealthServiceCheckResponse, error) {
	status := publicv1.ServingStatus_SERVING_STATUS_SERVING
	if s.readiness != nil && s.readiness() != nil {
		status = publicv1.ServingStatus_SERVING_STATUS_NOT_SERVING
	}
	return &publicv1.PublicHealthServiceCheckResponse{Status: status}, nil
}

type apiServerRunner struct {
	httpAddr          string
	grpcAddr          string
	privateGRPCAddr   string
	httpHandler       http.Handler
	grpcServer        grpcLifecycle
	privateGRPCServer grpcLifecycle
	readiness         *readinessGate
	shutdownTimeout   time.Duration
	listen            func(network, address string) (net.Listener, error)
	newHTTPServer     func(http.Handler) httpLifecycle
	onBound           func(httpListener, grpcListener, privateGRPCListener net.Listener)
}

type httpLifecycle interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
	Close() error
}

type grpcLifecycle interface {
	Serve(net.Listener) error
	GracefulStop()
	Stop()
}

func (r *apiServerRunner) run(ctx context.Context) error {
	listen := r.listen
	if listen == nil {
		listen = net.Listen
	}
	httpListener, err := listen("tcp", r.httpAddr)
	if err != nil {
		return fmt.Errorf("listen public HTTP %s: %w", r.httpAddr, err)
	}
	grpcListener, err := listen("tcp", r.grpcAddr)
	if err != nil {
		_ = httpListener.Close()
		return fmt.Errorf("listen public gRPC %s: %w", r.grpcAddr, err)
	}
	var privateGRPCListener net.Listener
	if r.privateGRPCAddr != "" {
		if r.privateGRPCServer == nil {
			_ = grpcListener.Close()
			_ = httpListener.Close()
			return errors.New("private gRPC address configured without server")
		}
		privateGRPCListener, err = listen("tcp", r.privateGRPCAddr)
		if err != nil {
			_ = grpcListener.Close()
			_ = httpListener.Close()
			return fmt.Errorf("listen private gateway gRPC %s: %w", r.privateGRPCAddr, err)
		}
	}

	newHTTPServer := r.newHTTPServer
	if newHTTPServer == nil {
		newHTTPServer = func(handler http.Handler) httpLifecycle {
			return &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
		}
	}
	httpServer := newHTTPServer(r.httpHandler)
	r.readiness.admitting.Store(true)
	if r.onBound != nil {
		r.onBound(httpListener, grpcListener, privateGRPCListener)
	}

	serveErrors := make(chan error, 3)
	go func() {
		if err := httpServer.Serve(httpListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrors <- fmt.Errorf("public HTTP serve: %w", err)
		}
	}()
	if privateGRPCListener != nil {
		go func() {
			if err := r.privateGRPCServer.Serve(privateGRPCListener); err != nil {
				serveErrors <- fmt.Errorf("private gateway gRPC serve: %w", err)
			}
		}()
	}
	go func() {
		if err := r.grpcServer.Serve(grpcListener); err != nil {
			serveErrors <- fmt.Errorf("public gRPC serve: %w", err)
		}
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serveErrors:
	}
	r.readiness.admitting.Store(false)

	timeout := r.shutdownTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	httpDone := make(chan error, 1)
	grpcDone := make(chan struct{})
	var privateGRPCDone chan struct{}
	if r.privateGRPCServer != nil {
		privateGRPCDone = make(chan struct{})
	}
	go func() { httpDone <- httpServer.Shutdown(shutdownCtx) }()
	go func() {
		r.grpcServer.GracefulStop()
		close(grpcDone)
	}()
	if privateGRPCDone != nil {
		go func() {
			r.privateGRPCServer.GracefulStop()
			close(privateGRPCDone)
		}()
	}

	var shutdownErr error
	for httpDone != nil || grpcDone != nil || privateGRPCDone != nil {
		select {
		case err := <-httpDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				shutdownErr = fmt.Errorf("public HTTP shutdown: %w", err)
			}
			httpDone = nil
		case <-grpcDone:
			grpcDone = nil
		case <-privateGRPCDone:
			privateGRPCDone = nil
		case <-shutdownCtx.Done():
			r.grpcServer.Stop()
			if r.privateGRPCServer != nil {
				r.privateGRPCServer.Stop()
			}
			_ = httpServer.Close()
			if shutdownErr == nil {
				shutdownErr = fmt.Errorf("public server shutdown timeout: %w", shutdownCtx.Err())
			}
			return errors.Join(serveErr, shutdownErr)
		}
	}
	return errors.Join(serveErr, shutdownErr)
}

// newPublicGRPCServer builds the public gRPC listener's server: the public.v1
// application surface (sessions/messages/events) over the same service graph
// the REST handlers use, plus the unauthenticated health service. gRPC
// reflection is deliberately NOT enabled — the public API is served from the
// committed public/v1 contracts only (docs/specs/grpc-contracts.md).
func newPublicGRPCServer(readiness func() error, deps apigrpc.Deps) *grpc.Server {
	server := apigrpc.RegisterServer(deps)
	publicv1.RegisterPublicHealthServiceServer(server, publicHealthService{readiness: readiness})
	return server
}
