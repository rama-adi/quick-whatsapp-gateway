package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestEngineClientRequiresExplicitDeadline(t *testing.T) {
	if _, err := NewEngineClient(nil, nil, 0, time.Second); err == nil {
		t.Fatal("zero deadline accepted")
	}
	if _, err := NewEngineClient(nil, nil, time.Second, time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestEngineClientPoolKeysGatewayAndEndpoint(t *testing.T) {
	var calls int
	client, err := NewEngineClient(nil, func(context.Context, string, string) (*grpc.ClientConn, error) {
		calls++
		return &grpc.ClientConn{}, nil
	}, time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.conn(context.Background(), "gw_1", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.conn(context.Background(), "gw_1", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.conn(context.Background(), "gw_1", "two"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.conn(context.Background(), "gw_2", "one"); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("dials=%d", calls)
	}
	if got := client.Health(); len(got) != 3 {
		t.Fatalf("health=%v", got)
	}
}

func TestMapEngineError(t *testing.T) {
	for _, tt := range []struct {
		code codes.Code
		want string
	}{
		{codes.NotFound, domain.CodeNotFound}, {codes.FailedPrecondition, domain.CodeConflict}, {codes.Unavailable, domain.CodeUnavailable}, {codes.InvalidArgument, domain.CodeValidationError},
	} {
		t.Run(tt.code.String(), func(t *testing.T) {
			var got *domain.APIError
			if !errors.As(mapEngineError(status.Error(tt.code, "x")), &got) || got.Code != tt.want {
				t.Fatalf("got %v", mapEngineError(status.Error(tt.code, "x")))
			}
		})
	}
	if mapEngineError(context.DeadlineExceeded) == nil {
		t.Fatal("deadline lost")
	}
}
