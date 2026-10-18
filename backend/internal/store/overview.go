package store

import (
	"context"
	"fmt"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
)

// OverviewWindow selects the activity the overview counts. Bounds are the
// ascending epoch-ms bucket starts (Bounds[0] is the window start); ToMs is the
// exclusive window end. SessionIDs scopes message and outbox counts and must be
// non-empty; SessionID, when set, also scopes webhook deliveries to events of
// that one session.
type OverviewWindow struct {
	OrganizationID string
	SessionID      string
	SessionIDs     []string
	Bounds         []int64
	ToMs           int64
}

// OverviewActivity holds per-bucket counts aligned with OverviewWindow.Bounds,
// plus the distinct active chat count for the whole window.
type OverviewActivity struct {
	MessagesReceived  []int64
	MessagesSent      []int64
	SendsFailed       []int64
	WebhookDeliveries []int64
	WebhookFailures   []int64
	ActiveChats       int64
}

// OverviewRepo serves the read-only aggregate queries behind GET /overview.
// Bucket alignment is decided by the caller; the queries only assign rows to
// the supplied boundaries with MySQL INTERVAL().
type OverviewRepo struct {
	q *storedb.Queries
}

// NewOverviewRepo constructs an OverviewRepo over a database or transaction.
func NewOverviewRepo(db storedb.DBTX) *OverviewRepo { return &OverviewRepo{q: storedb.New(db)} }

// OrgSessionIDs returns the ids of every session the organization owns.
func (r *OverviewRepo) OrgSessionIDs(ctx context.Context, organizationID string) ([]string, error) {
	ids, err := r.q.OverviewOrgSessionIDs(ctx, storedb.OverviewOrgSessionIDsParams{OrganizationID: organizationID})
	if err != nil {
		return nil, fmt.Errorf("store: overview session ids: %w", err)
	}
	return ids, nil
}

// Activity counts messages, failed sends, and webhook delivery outcomes per
// bucket. Rows outside [Bounds[0], ToMs) are never read.
func (r *OverviewRepo) Activity(ctx context.Context, w OverviewWindow) (OverviewActivity, error) {
	n := len(w.Bounds)
	out := OverviewActivity{
		MessagesReceived:  make([]int64, n),
		MessagesSent:      make([]int64, n),
		SendsFailed:       make([]int64, n),
		WebhookDeliveries: make([]int64, n),
		WebhookFailures:   make([]int64, n),
	}
	if n == 0 {
		return out, nil
	}
	fromMs := w.Bounds[0]
	// INTERVAL(x, b1..bn) returns 0 for x < b1, so the first bucket start is
	// implicit and only the later starts are passed.
	bounds := make([]any, 0, n-1)
	for _, b := range w.Bounds[1:] {
		bounds = append(bounds, b)
	}
	if len(bounds) == 0 {
		// A single bucket: INTERVAL needs at least one bound; the window end puts
		// every in-window row at index 0.
		bounds = append(bounds, w.ToMs)
	}
	add := func(series []int64, bucket, count int64) {
		if bucket >= 0 && bucket < int64(n) {
			series[bucket] += count
		}
	}

	if len(w.SessionIDs) > 0 {
		msgs, err := r.q.OverviewMessageBuckets(ctx, storedb.OverviewMessageBucketsParams{
			Bounds: bounds, SessionIds: w.SessionIDs, FromMs: fromMs, ToMs: w.ToMs,
		})
		if err != nil {
			return out, fmt.Errorf("store: overview message buckets: %w", err)
		}
		for _, row := range msgs {
			switch domain.MessageDirection(row.Direction) {
			case domain.DirectionIn:
				add(out.MessagesReceived, row.Bucket, row.N)
			case domain.DirectionOut:
				add(out.MessagesSent, row.Bucket, row.N)
			}
		}

		chats, err := r.q.OverviewActiveChats(ctx, storedb.OverviewActiveChatsParams{
			SessionIds: w.SessionIDs, FromMs: fromMs, ToMs: w.ToMs,
		})
		if err != nil {
			return out, fmt.Errorf("store: overview active chats: %w", err)
		}
		out.ActiveChats = chats

		failed, err := r.q.OverviewFailedSendBuckets(ctx, storedb.OverviewFailedSendBucketsParams{
			Bounds: bounds, OrganizationID: w.OrganizationID, FromMs: fromMs, ToMs: w.ToMs, SessionIds: w.SessionIDs,
		})
		if err != nil {
			return out, fmt.Errorf("store: overview failed sends: %w", err)
		}
		for _, row := range failed {
			add(out.SendsFailed, row.Bucket, row.N)
		}
	}

	webhookIDs, err := r.q.OverviewOrgWebhookIDs(ctx, storedb.OverviewOrgWebhookIDsParams{OrganizationID: w.OrganizationID})
	if err != nil {
		return out, fmt.Errorf("store: overview webhook ids: %w", err)
	}
	if len(webhookIDs) == 0 {
		return out, nil
	}
	type deliveryRow struct {
		bucket int64
		status storedb.WebhookDeliveriesStatus
		n      int64
	}
	var deliveries []deliveryRow
	if w.SessionID == "" {
		rows, err := r.q.OverviewWebhookDeliveryBuckets(ctx, storedb.OverviewWebhookDeliveryBucketsParams{
			Bounds: bounds, WebhookIds: webhookIDs, FromMs: fromMs, ToMs: w.ToMs,
		})
		if err != nil {
			return out, fmt.Errorf("store: overview webhook deliveries: %w", err)
		}
		for _, row := range rows {
			deliveries = append(deliveries, deliveryRow{row.Bucket, row.Status, row.N})
		}
	} else {
		rows, err := r.q.OverviewSessionWebhookDeliveryBuckets(ctx, storedb.OverviewSessionWebhookDeliveryBucketsParams{
			Bounds: bounds, WebhookIds: webhookIDs, SessionID: w.SessionID, FromMs: fromMs, ToMs: w.ToMs,
		})
		if err != nil {
			return out, fmt.Errorf("store: overview session webhook deliveries: %w", err)
		}
		for _, row := range rows {
			deliveries = append(deliveries, deliveryRow{row.Bucket, row.Status, row.N})
		}
	}
	for _, row := range deliveries {
		switch domain.WebhookDeliveryStatus(row.status) {
		case domain.DeliveryDelivered:
			add(out.WebhookDeliveries, row.bucket, row.n)
		case domain.DeliveryFailed, domain.DeliveryDead:
			add(out.WebhookFailures, row.bucket, row.n)
		}
	}
	return out, nil
}

// HasSentMessage reports whether the organization has ever sent a message: an
// outbox command that reached "sent", or any outgoing message stored for one of
// sessionIDs (which also covers messages sent from the phone itself).
func (r *OverviewRepo) HasSentMessage(ctx context.Context, organizationID string, sessionIDs []string) (bool, error) {
	sent, err := r.q.OverviewHasSentOutbox(ctx, storedb.OverviewHasSentOutboxParams{OrganizationID: organizationID})
	if err != nil {
		return false, fmt.Errorf("store: overview has sent outbox: %w", err)
	}
	if sent || len(sessionIDs) == 0 {
		return sent, nil
	}
	sent, err = r.q.OverviewHasOutgoingMessage(ctx, storedb.OverviewHasOutgoingMessageParams{SessionIds: sessionIDs})
	if err != nil {
		return false, fmt.Errorf("store: overview has outgoing message: %w", err)
	}
	return sent, nil
}
