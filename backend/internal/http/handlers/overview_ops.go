package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/humax"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/service"
)

// getOverviewInput is GET /overview.
type getOverviewInput struct {
	Range   string `query:"range" default:"7d" enum:"24h,7d,14d,30d" doc:"How far back to look. 24h returns the last 24 hours in hourly buckets; 7d, 14d and 30d return the last 7, 14 or 30 days in daily buckets (today included, so the last bucket is partial)." example:"7d"`
	TZ      string `query:"tz" default:"UTC" doc:"IANA time zone name used to align buckets: hourly buckets start on the hour and daily buckets start at midnight in this zone. Pass the viewer's zone so days match their calendar. An unknown zone fails with validation_error." example:"Asia/Jakarta"`
	Session string `query:"session" doc:"Optional session id. When set, messages, active chats, failed sends and webhook deliveries are counted for this session only. Must belong to the caller's organization, otherwise the request fails with not_found. Omit to count all of the organization's sessions." example:"01HHX5SESSION..."`
}

type overviewOutput struct{ Body apitypes.Overview }

// RegisterOverviewOps registers the workspace overview (read capability).
func RegisterOverviewOps(api huma.API, h *Handlers) {
	read := huma.Middlewares{humax.RequireCap(api, authz.CapRead)}

	huma.Register(api, huma.Operation{
		OperationID: "getOverview", Method: "GET", Path: "/api/v1/overview",
		Summary: "Get the workspace overview", Tags: []string{"Overview"}, Middlewares: read,
		Description: "Returns an activity summary for the caller's organization over a recent window: messages received and sent, active chats, failed sends, and webhook delivery outcomes. " +
			"Each count is returned as a window total and as a per-bucket series for charts. Requires the `read` capability.\n\n" +
			"**Buckets.** `range=24h` returns 24 hourly buckets; `7d`, `14d` and `30d` return 7, 14 or 30 daily buckets. Buckets are aligned to the `tz` time zone and listed in `buckets` as epoch-millisecond start times, oldest first. " +
			"The last bucket contains the current moment, so it is partial. Every series has one entry per bucket, and buckets without activity are 0.\n\n" +
			"**What is counted.** Messages are bucketed by their WhatsApp timestamp. Failed sends and webhook deliveries are bucketed by when they were created, using their current status. " +
			"`hasSentMessage` and `retentionDays` describe the organization and do not depend on the window. Session states, API keys and webhook configuration are not included; use `GET /sessions` and `GET /webhooks` for those.\n\n" +
			"Errors: `validation_error` if `range` or `tz` is invalid; `not_found` if `session` is not owned by the caller's organization; `forbidden` if the caller lacks the `read` capability.",
	}, func(ctx context.Context, in *getOverviewInput) (*overviewOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		overview, err := h.Overview.Get(ctx, org, service.OverviewQuery{
			Range:     apitypes.OverviewRange(in.Range),
			TZ:        in.TZ,
			SessionID: in.Session,
		})
		if err != nil {
			return nil, humax.ErrContext(ctx, err)
		}
		return &overviewOutput{Body: overview}, nil
	})
}
