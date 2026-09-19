package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	publicv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/public/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/apigrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type fakeHTTPLifecycle struct {
	serveErr       error
	serveStarted   chan struct{}
	shutdownCalled chan struct{}
	shutdownBlock  <-chan struct{}
	stopped        chan struct{}
	stopOnce       sync.Once
	mu             sync.Mutex
	listener       net.Listener
}

func newFakeHTTP() *fakeHTTPLifecycle {
	return &fakeHTTPLifecycle{serveStarted: make(chan struct{}), shutdownCalled: make(chan struct{}), stopped: make(chan struct{})}
}

func (f *fakeHTTPLifecycle) Serve(listener net.Listener) error {
	f.mu.Lock()
	f.listener = listener
	f.mu.Unlock()
	close(f.serveStarted)
	if f.serveErr != nil {
		return f.serveErr
	}
	<-f.stopped
	return http.ErrServerClosed
}

func (f *fakeHTTPLifecycle) Shutdown(ctx context.Context) error {
	close(f.shutdownCalled)
	if f.shutdownBlock != nil {
		select {
		case <-f.shutdownBlock:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.Close()
}

func (f *fakeHTTPLifecycle) Close() error {
	f.stopOnce.Do(func() { close(f.stopped) })
	f.mu.Lock()
	listener := f.listener
	f.mu.Unlock()
	if listener != nil {
		return listener.Close()
	}
	return nil
}

type fakeGRPCLifecycle struct {
	serveErr       error
	serveStarted   chan struct{}
	gracefulCalled chan struct{}
	gracefulBlock  <-chan struct{}
	stopped        chan struct{}
	stopOnce       sync.Once
	stopCalled     atomic.Bool
	mu             sync.Mutex
	listener       net.Listener
}

func newFakeGRPC() *fakeGRPCLifecycle {
	return &fakeGRPCLifecycle{serveStarted: make(chan struct{}), gracefulCalled: make(chan struct{}), stopped: make(chan struct{})}
}

func (f *fakeGRPCLifecycle) Serve(listener net.Listener) error {
	f.mu.Lock()
	f.listener = listener
	f.mu.Unlock()
	close(f.serveStarted)
	if f.serveErr != nil {
		return f.serveErr
	}
	<-f.stopped
	return nil
}

func (f *fakeGRPCLifecycle) GracefulStop() {
	close(f.gracefulCalled)
	if f.gracefulBlock != nil {
		select {
		case <-f.gracefulBlock:
		case <-f.stopped:
			return
		}
	}
	f.stop()
}

func (f *fakeGRPCLifecycle) Stop() {
	f.stopCalled.Store(true)
	f.stop()
}

func (f *fakeGRPCLifecycle) stop() {
	f.stopOnce.Do(func() {
		close(f.stopped)
		f.mu.Lock()
		listener := f.listener
		f.mu.Unlock()
		if listener != nil {
			_ = listener.Close()
		}
	})
}

func TestPublicHealthServiceServingAndUnready(t *testing.T) {
	ready := true
	service := publicHealthService{readiness: func() error {
		if !ready {
			return errors.New("dependency unavailable")
		}
		return nil
	}}
	response, err := service.Check(context.Background(), &publicv1.PublicHealthServiceCheckRequest{})
	if err != nil || response.Status != publicv1.ServingStatus_SERVING_STATUS_SERVING {
		t.Fatalf("ready response = (%v, %v)", response, err)
	}
	ready = false
	response, err = service.Check(context.Background(), &publicv1.PublicHealthServiceCheckRequest{})
	if err != nil || response.Status != publicv1.ServingStatus_SERVING_STATUS_NOT_SERVING {
		t.Fatalf("unready response = (%v, %v)", response, err)
	}
}

func TestAPIServerRunnerServesBothListenersAndShutsDown(t *testing.T) {
	dependenciesReady := true
	gate := &readinessGate{dependencies: func() error {
		if !dependenciesReady {
			return errors.New("dependency unavailable")
		}
		return nil
	}}
	grpcServer := newPublicGRPCServer(gate.check, apigrpc.Deps{})
	bound := make(chan [2]net.Listener, 1)
	runner := &apiServerRunner{
		httpAddr: "127.0.0.1:0", grpcAddr: "127.0.0.1:0",
		httpHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if err := gate.check(); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, "ready")
		}),
		grpcServer: grpcServer, readiness: gate, shutdownTimeout: time.Second,
		onBound: func(httpListener, grpcListener, _ net.Listener) {
			bound <- [2]net.Listener{httpListener, grpcListener}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.run(ctx) }()
	listeners := <-bound

	response, err := http.Get("http://" + listeners[0].Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status = %d", response.StatusCode)
	}

	conn, err := grpc.NewClient(listeners[1].Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	client := publicv1.NewPublicHealthServiceClient(conn)
	health, err := client.Check(context.Background(), &publicv1.PublicHealthServiceCheckRequest{})
	if err != nil || health.Status != publicv1.ServingStatus_SERVING_STATUS_SERVING {
		t.Fatalf("gRPC health = (%v, %v)", health, err)
	}
	dependenciesReady = false
	health, err = client.Check(context.Background(), &publicv1.PublicHealthServiceCheckRequest{})
	if err != nil || health.Status != publicv1.ServingStatus_SERVING_STATUS_NOT_SERVING {
		t.Fatalf("unready gRPC health = (%v, %v)", health, err)
	}
	_ = conn.Close()

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runner shutdown: %v", err)
	}
	if gate.admitting.Load() {
		t.Fatal("readiness remained true during drain")
	}
}

func TestAPIServerRunnerBindFailureClosesPriorListener(t *testing.T) {
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := &apiServerRunner{
		httpAddr: "http", grpcAddr: "grpc", httpHandler: http.NotFoundHandler(),
		grpcServer: grpc.NewServer(), readiness: &readinessGate{},
		listen: func(_, _ string) (net.Listener, error) {
			calls++
			if calls == 1 {
				return first, nil
			}
			return nil, errors.New("address in use")
		},
	}
	err = runner.run(context.Background())
	if err == nil || err.Error() != "listen public gRPC grpc: address in use" {
		t.Fatalf("bind error = %v", err)
	}
	if closeErr := first.Close(); !errors.Is(closeErr, net.ErrClosed) {
		t.Fatalf("first listener was not closed, close error = %v", closeErr)
	}
}

func TestPublicGRPCServerRegistersNoGatewayService(t *testing.T) {
	server := newPublicGRPCServer(func() error { return nil }, apigrpc.Deps{})
	services := server.GetServiceInfo()
	if _, ok := services[publicv1.PublicHealthService_ServiceDesc.ServiceName]; !ok {
		t.Fatal("public health service is not registered")
	}
	if _, ok := services[gatewayv1.GatewayHealthService_ServiceDesc.ServiceName]; ok {
		t.Fatal("private gateway service registered on public server")
	}
	if _, ok := services[gatewayv1.GatewayEnrollmentService_ServiceDesc.ServiceName]; ok {
		t.Fatal("private enrollment service registered on public server")
	}
	// Increment 8: the public surface serves sessions, messages, and events
	// alongside health. The private gateway.v1 domain must never appear.
	for _, name := range []string{
		publicv1.PublicSessionsService_ServiceDesc.ServiceName,
		publicv1.PublicMessagesService_ServiceDesc.ServiceName,
		publicv1.PublicEventsService_ServiceDesc.ServiceName,
	} {
		if _, ok := services[name]; !ok {
			t.Fatalf("public service %s is not registered", name)
		}
	}
	if len(services) != 4 {
		t.Fatalf("public services = %v, want exactly the four public.v1 services", services)
	}
}

func TestAPIServerRunnerThirdBindFailureClosesPriorListeners(t *testing.T) {
	first, _ := net.Listen("tcp", "127.0.0.1:0")
	second, _ := net.Listen("tcp", "127.0.0.1:0")
	calls := 0
	runner := &apiServerRunner{httpAddr: "http", grpcAddr: "public", privateGRPCAddr: "private", httpHandler: http.NotFoundHandler(), grpcServer: grpc.NewServer(), privateGRPCServer: grpc.NewServer(), readiness: &readinessGate{}, listen: func(_, _ string) (net.Listener, error) {
		calls++
		switch calls {
		case 1:
			return first, nil
		case 2:
			return second, nil
		default:
			return nil, errors.New("address in use")
		}
	}}
	err := runner.run(context.Background())
	if err == nil || err.Error() != "listen private gateway gRPC private: address in use" {
		t.Fatalf("bind error = %v", err)
	}
	for _, listener := range []net.Listener{first, second} {
		if closeErr := listener.Close(); !errors.Is(closeErr, net.ErrClosed) {
			t.Fatalf("listener remained open: %v", closeErr)
		}
	}
}

func TestAPIServerRunnerPrivateServeFailureAndDrain(t *testing.T) {
	httpServer := newFakeHTTP()
	publicServer := newFakeGRPC()
	privateServer := newFakeGRPC()
	privateServer.serveErr = errors.New("private boom")
	runner := &apiServerRunner{httpAddr: "127.0.0.1:0", grpcAddr: "127.0.0.1:0", privateGRPCAddr: "127.0.0.1:0", httpHandler: http.NotFoundHandler(), grpcServer: publicServer, privateGRPCServer: privateServer, readiness: &readinessGate{}, shutdownTimeout: time.Second, newHTTPServer: func(http.Handler) httpLifecycle { return httpServer }}
	err := runner.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "private gateway gRPC serve: private boom") {
		t.Fatalf("serve error = %v", err)
	}
	select {
	case <-publicServer.gracefulCalled:
	default:
		t.Fatal("public server did not drain")
	}
	select {
	case <-privateServer.gracefulCalled:
	default:
		t.Fatal("private server did not drain")
	}
}

func TestAPIServerRunnerAttributesServeFailures(t *testing.T) {
	tests := []struct {
		name, want string
		httpErr    error
		grpcErr    error
	}{
		{"HTTP", "public HTTP serve: http boom", errors.New("http boom"), nil},
		{"gRPC", "public gRPC serve: grpc boom", nil, errors.New("grpc boom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpServer := newFakeHTTP()
			httpServer.serveErr = tt.httpErr
			grpcServer := newFakeGRPC()
			grpcServer.serveErr = tt.grpcErr
			runner := &apiServerRunner{
				httpAddr: "127.0.0.1:0", grpcAddr: "127.0.0.1:0", httpHandler: http.NotFoundHandler(),
				grpcServer: grpcServer, readiness: &readinessGate{}, shutdownTimeout: time.Second,
				newHTTPServer: func(http.Handler) httpLifecycle { return httpServer },
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := runner.run(ctx)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("runner error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestAPIServerRunnerWaitsForGracefulHTTPAndGRPCDrain(t *testing.T) {
	httpRelease := make(chan struct{})
	grpcRelease := make(chan struct{})
	httpServer := newFakeHTTP()
	httpServer.shutdownBlock = httpRelease
	grpcServer := newFakeGRPC()
	grpcServer.gracefulBlock = grpcRelease
	bound := make(chan struct{})
	runner := &apiServerRunner{
		httpAddr: "127.0.0.1:0", grpcAddr: "127.0.0.1:0", httpHandler: http.NotFoundHandler(),
		grpcServer: grpcServer, readiness: &readinessGate{}, shutdownTimeout: time.Second,
		newHTTPServer: func(http.Handler) httpLifecycle { return httpServer },
		onBound:       func(net.Listener, net.Listener, net.Listener) { close(bound) },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.run(ctx) }()
	<-bound
	cancel()
	<-httpServer.shutdownCalled
	<-grpcServer.gracefulCalled
	select {
	case err := <-done:
		t.Fatalf("runner returned before drains completed: %v", err)
	default:
	}
	close(httpRelease)
	close(grpcRelease)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not finish graceful drain")
	}
}

func TestAPIServerRunnerForcesStopAfterTimeout(t *testing.T) {
	never := make(chan struct{})
	httpServer := newFakeHTTP()
	httpServer.shutdownBlock = never
	grpcServer := newFakeGRPC()
	grpcServer.gracefulBlock = never
	bound := make(chan struct{})
	runner := &apiServerRunner{
		httpAddr: "127.0.0.1:0", grpcAddr: "127.0.0.1:0", httpHandler: http.NotFoundHandler(),
		grpcServer: grpcServer, readiness: &readinessGate{}, shutdownTimeout: 20 * time.Millisecond,
		newHTTPServer: func(http.Handler) httpLifecycle { return httpServer },
		onBound:       func(net.Listener, net.Listener, net.Listener) { close(bound) },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.run(ctx) }()
	<-bound
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "public server shutdown timeout") {
			t.Fatalf("forced shutdown error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forced shutdown hung")
	}
	if !grpcServer.stopCalled.Load() {
		t.Fatal("gRPC Stop was not called")
	}
}
