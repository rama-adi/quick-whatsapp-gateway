package service

import (
	"context"
	"fmt"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

// OverviewStore is the read model behind the workspace overview.
type OverviewStore interface {
	OrgSessionIDs(ctx context.Context, organizationID string) ([]string, error)
	Activity(ctx context.Context, w store.OverviewWindow) (store.OverviewActivity, error)
	HasSentMessage(ctx context.Context, organizationID string, sessionIDs []string) (bool, error)
}

// OverviewSessionLookup resolves a session so the optional filter can be
// checked against the caller's organization.
type OverviewSessionLookup interface {
	Get(ctx context.Context, id string) (domain.WASession, error)
}

// OverviewQuery is the validated-at-the-edge input of GET /overview. Range and
// TZ are re-validated here so the service is safe to call directly.
type OverviewQuery struct {
	Range     apitypes.OverviewRange
	TZ        string
	SessionID string
}

// OverviewService builds the org-scoped activity summary for the dashboard
// home page from MySQL projections.
type OverviewService struct {
	repo          OverviewStore
	sessions      OverviewSessionLookup
	retentionDays int
	now           func() time.Time
}

// NewOverviewService constructs an OverviewService. retentionDays is the
// configured RETENTION_DAYS (0 = keep forever), reported back to clients.
func NewOverviewService(repo OverviewStore, sessions OverviewSessionLookup, retentionDays int) *OverviewService {
	return &OverviewService{repo: repo, sessions: sessions, retentionDays: retentionDays, now: time.Now}
}

// SetClock overrides the wall clock (tests).
func (s *OverviewService) SetClock(now func() time.Time) { s.now = now }

// Get returns the overview for the organization over the requested window.
func (s *OverviewService) Get(ctx context.Context, organizationID string, q OverviewQuery) (apitypes.Overview, error) {
	if q.Range == "" {
		q.Range = apitypes.OverviewRange7d
	}
	if q.TZ == "" {
		q.TZ = "UTC"
	}
	loc, err := loadOverviewLocation(q.TZ)
	if err != nil {
		return apitypes.Overview{}, err
	}
	now := s.now()
	bounds, err := OverviewBuckets(q.Range, now, loc)
	if err != nil {
		return apitypes.Overview{}, err
	}

	orgSessionIDs, err := s.repo.OrgSessionIDs(ctx, organizationID)
	if err != nil {
		return apitypes.Overview{}, err
	}
	scoped := orgSessionIDs
	if q.SessionID != "" {
		sess, err := s.sessions.Get(ctx, q.SessionID)
		if err != nil {
			return apitypes.Overview{}, err
		}
		if sess.OrganizationID != organizationID {
			return apitypes.Overview{}, domain.ErrNotFound("session not found")
		}
		scoped = []string{q.SessionID}
	}

	bucketMs := make([]int64, len(bounds))
	for i, b := range bounds {
		bucketMs[i] = b.UnixMilli()
	}
	toMs := now.UnixMilli()
	activity, err := s.repo.Activity(ctx, store.OverviewWindow{
		OrganizationID: organizationID,
		SessionID:      q.SessionID,
		SessionIDs:     scoped,
		Bounds:         bucketMs,
		ToMs:           toMs,
	})
	if err != nil {
		return apitypes.Overview{}, err
	}
	hasSent, err := s.repo.HasSentMessage(ctx, organizationID, orgSessionIDs)
	if err != nil {
		return apitypes.Overview{}, err
	}

	return apitypes.Overview{
		Range:   q.Range,
		TZ:      q.TZ,
		From:    bucketMs[0],
		To:      toMs,
		Buckets: bucketMs,
		Totals: apitypes.OverviewTotals{
			MessagesReceived:  sum(activity.MessagesReceived),
			MessagesSent:      sum(activity.MessagesSent),
			ActiveChats:       activity.ActiveChats,
			SendsFailed:       sum(activity.SendsFailed),
			WebhookDeliveries: sum(activity.WebhookDeliveries),
			WebhookFailures:   sum(activity.WebhookFailures),
		},
		Series: apitypes.OverviewSeries{
			MessagesReceived:  activity.MessagesReceived,
			MessagesSent:      activity.MessagesSent,
			SendsFailed:       activity.SendsFailed,
			WebhookDeliveries: activity.WebhookDeliveries,
			WebhookFailures:   activity.WebhookFailures,
		},
		HasSentMessage: hasSent,
		RetentionDays:  s.retentionDays,
	}, nil
}

// loadOverviewLocation accepts IANA zone names only. "Local" is rejected
// because it would silently mean the server's zone.
func loadOverviewLocation(name string) (*time.Location, error) {
	if name == "Local" {
		return nil, domain.ErrValidation("tz must be an IANA time zone name such as UTC or Asia/Jakarta")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, domain.ErrValidation(fmt.Sprintf("tz %q is not a known IANA time zone", name))
	}
	return loc, nil
}

// OverviewBuckets returns the start of every bucket of the window ending at now,
// oldest first. 24h yields 24 buckets starting on local hours; 7d/14d/30d yield
// 7/14/30 buckets starting at local midnight. The last bucket contains now.
//
// Boundaries come from the local calendar in loc, not from fixed 3600s/86400s
// steps, so they stay aligned on daylight-saving days (a 23h or 25h day is one
// daily bucket) and in zones with half-hour or 45-minute offsets.
func OverviewBuckets(r apitypes.OverviewRange, now time.Time, loc *time.Location) ([]time.Time, error) {
	local := now.In(loc)
	switch r {
	case apitypes.OverviewRange24h:
		const n = 24
		out := make([]time.Time, n)
		start := truncateLocalHour(local)
		out[n-1] = start
		for i := n - 2; i >= 0; i-- {
			// One real hour back lands inside the previous local hour (or the
			// repeated hour on a fall-back day); truncating to its local :00
			// keeps every boundary on a wall-clock hour.
			start = truncateLocalHour(start.Add(-time.Hour))
			out[i] = start
		}
		return out, nil
	case apitypes.OverviewRange7d, apitypes.OverviewRange14d, apitypes.OverviewRange30d:
		n := map[apitypes.OverviewRange]int{
			apitypes.OverviewRange7d: 7, apitypes.OverviewRange14d: 14, apitypes.OverviewRange30d: 30,
		}[r]
		out := make([]time.Time, n)
		y, m, d := local.Date()
		for i := range n {
			// time.Date normalizes the day overflow and, where midnight does not
			// exist (DST at 00:00), resolves to the first instant of that day.
			out[i] = time.Date(y, m, d-(n-1-i), 0, 0, 0, 0, loc)
		}
		return out, nil
	default:
		return nil, domain.ErrValidation("range must be one of 24h, 7d, 14d, 30d")
	}
}

// truncateLocalHour returns the instant the local clock last read HH:00.
func truncateLocalHour(t time.Time) time.Time {
	return t.Add(-(time.Duration(t.Minute())*time.Minute +
		time.Duration(t.Second())*time.Second +
		time.Duration(t.Nanosecond())))
}

func sum(values []int64) int64 {
	var total int64
	for _, v := range values {
		total += v
	}
	return total
}
