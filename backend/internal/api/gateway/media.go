package gateway

import (
	"context"
	gatewayv1 "github.com/rama-adi/quick-whatsapp-gateway/gen/gateway/v1"
)

func (c *EngineClient) DownloadMedia(ctx context.Context, org, session string, source []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.sendDeadline)
	defer cancel()
	target, err := c.resolver.ResolveSessionEngineTarget(ctx, org, session)
	if err != nil {
		return nil, err
	}
	conn, err := c.conn(ctx, target.GatewayID, target.GRPCEndpoint)
	if err != nil {
		return nil, err
	}
	result, err := gatewayv1.NewGatewayEngineServiceClient(conn).DownloadMedia(ctx, &gatewayv1.DownloadMediaRequest{Target: sessionTargetProto(target), AssignmentEpoch: target.AssignmentEpoch, Source: source})
	if err != nil {
		return nil, mapEngineError(err)
	}
	return result.Data, nil
}
