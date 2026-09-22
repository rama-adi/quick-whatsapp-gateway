package enginegrpc

import (
	"context"
	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/application"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) DownloadMedia(ctx context.Context, req *gatewayv1.DownloadMediaRequest) (*gatewayv1.DownloadMediaResponse, error) {
	target, err := s.target(req.GetTarget())
	if err != nil {
		return nil, grpcError(err)
	}
	downloader, ok := s.Engine.(interface {
		DownloadMedia(context.Context, application.SessionStateQuery, []byte) ([]byte, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "media download unavailable")
	}
	if req.GetAssignmentEpoch() == 0 {
		return nil, status.Error(codes.InvalidArgument, "assignment epoch required")
	}
	data, err := downloader.DownloadMedia(ctx, sessionQuery(target, req.GetAssignmentEpoch()), req.GetSource())
	if err != nil {
		return nil, grpcError(err)
	}
	return &gatewayv1.DownloadMediaResponse{Data: data}, nil
}
