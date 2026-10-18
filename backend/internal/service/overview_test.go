package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
}

func utc(y int, m time.Month, d, h, minute int) time.Time {
	return time.Date(y, m, d, h, minute, 0, 0, time.UTC)
}

// assertWindow checks the invariants every window must hold: n strictly
// increasing boundaries, with now inside the last bucket.
func assertWindow(t *testing.T, b []time.Time, n int, now time.Time) {
	t.Helper()
	if len(b) != n {
		t.Fatalf("len = %d, want %d", len(b), n)
	}
	for i := 1; i < len(b); i++ {
		if !b[i].After(b[i-1]) {
			t.Fatalf("bucket %d (%s) not after %d (%s)", i, b[i], i-1, b[i-1])
		}
	}
	if now.Before(b[n-1]) {
		t.Fatalf("now %s precedes last bucket %s", now, b[n-1])
	}
}

func TestOverviewBuckets_HourlyUTC(t *testing.T) {
	now := time.Date(2025, 10, 16, 7, 33, 20, 500, time.UTC)
	b, err := OverviewBuckets(apitypes.OverviewRange24h, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, b, 24, now)
	if !b[23].Equal(utc(2025, 10, 16, 7, 0)) || !b[0].Equal(utc(2025, 10, 15, 8, 0)) {
		t.Fatalf("first=%s last=%s", b[0], b[23])
	}
	for i := 1; i < 24; i++ {
		if b[i].Sub(b[i-1]) != time.Hour {
			t.Fatalf("bucket %d spans %s", i-1, b[i].Sub(b[i-1]))
		}
	}
}

func TestOverviewBuckets_HourlyHalfAndQuarterHourZones(t *testing.T) {
	now := time.Date(2025, 10, 16, 7, 33, 20, 0, time.UTC)
	for _, tc := range []struct {
		zone string
		last time.Time
	}{
		{"Asia/Kolkata", utc(2025, 10, 16, 7, 30)},   // 13:03 IST -> 13:00 IST
		{"Asia/Kathmandu", utc(2025, 10, 16, 7, 15)}, // 13:18 NPT -> 13:00 NPT
	} {
		loc := mustLoc(t, tc.zone)
		b, err := OverviewBuckets(apitypes.OverviewRange24h, now, loc)
		if err != nil {
			t.Fatal(err)
		}
		assertWindow(t, b, 24, now)
		if !b[23].Equal(tc.last) {
			t.Fatalf("%s last = %s, want %s", tc.zone, b[23], tc.last)
		}
		for i, start := range b {
			if l := start.In(loc); l.Minute() != 0 || l.Second() != 0 {
				t.Fatalf("%s bucket %d starts at local %s", tc.zone, i, l)
			}
		}
	}
}

func TestOverviewBuckets_HourlyAcrossFallBack(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// 2025-11-02: 02:00 EDT -> 01:00 EST at 06:00Z. now = 03:15 EST.
	now := utc(2025, 11, 2, 8, 15)
	b, err := OverviewBuckets(apitypes.OverviewRange24h, now, ny)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, b, 24, now)
	for i := 1; i < 24; i++ {
		if b[i].Sub(b[i-1]) != time.Hour {
			t.Fatalf("bucket %d spans %s", i-1, b[i].Sub(b[i-1]))
		}
	}
	// Both 01:00 EDT (05:00Z) and 01:00 EST (06:00Z) start a bucket.
	if !b[20].Equal(utc(2025, 11, 2, 5, 0)) || !b[21].Equal(utc(2025, 11, 2, 6, 0)) {
		t.Fatalf("repeated hour buckets = %s, %s", b[20], b[21])
	}
}

func TestOverviewBuckets_DailySpringForwardAndFallBack(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	for _, tc := range []struct {
		name   string
		now    time.Time
		day    int // local day-of-month with the DST change
		length time.Duration
	}{
		{"spring forward", utc(2025, 3, 10, 12, 0), 9, 23 * time.Hour},
		{"fall back", utc(2025, 11, 3, 12, 0), 2, 25 * time.Hour},
	} {
		b, err := OverviewBuckets(apitypes.OverviewRange7d, tc.now, ny)
		if err != nil {
			t.Fatal(err)
		}
		assertWindow(t, b, 7, tc.now)
		for i, start := range b {
			l := start.In(ny)
			if l.Hour() != 0 || l.Minute() != 0 {
				t.Fatalf("%s: bucket %d starts at local %s", tc.name, i, l)
			}
		}
		// The DST day is the second-to-last bucket.
		if got := b[5].In(ny).Day(); got != tc.day {
			t.Fatalf("%s: bucket 5 is day %d, want %d", tc.name, got, tc.day)
		}
		if got := b[6].Sub(b[5]); got != tc.length {
			t.Fatalf("%s: DST day spans %s, want %s", tc.name, got, tc.length)
		}
	}
}

func TestOverviewBuckets_DailyHalfHourZoneAndMonthBoundary(t *testing.T) {
	kolkata := mustLoc(t, "Asia/Kolkata")
	// 2025-03-02 05:00Z = 10:30 IST on March 2.
	now := utc(2025, 3, 2, 5, 0)
	b, err := OverviewBuckets(apitypes.OverviewRange30d, now, kolkata)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, b, 30, now)
	// Local midnight in IST is 18:30Z the previous day.
	if !b[29].Equal(utc(2025, 3, 1, 18, 30)) {
		t.Fatalf("last = %s", b[29])
	}
	if first := b[0].In(kolkata); first.Month() != time.February || first.Day() != 1 {
		t.Fatalf("first = %s", first)
	}
	for i := 1; i < 30; i++ {
		if b[i].Sub(b[i-1]) != 24*time.Hour {
			t.Fatalf("bucket %d spans %s", i-1, b[i].Sub(b[i-1]))
		}
	}
}

func TestOverviewBuckets_RangeLengthsAndValidation(t *testing.T) {
	now := utc(2025, 10, 16, 0, 0) // exactly midnight: a zero-length last bucket start
	for r, n := range map[apitypes.OverviewRange]int{"24h": 24, "7d": 7, "14d": 14, "30d": 30} {
		b, err := OverviewBuckets(r, now, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		assertWindow(t, b, n, now)
		if !b[n-1].Equal(now) {
			t.Fatalf("%s: last bucket %s should start at now on a boundary", r, b[n-1])
		}
	}
	_, err := OverviewBuckets("90d", now, time.UTC)
	assertCode(t, err, domain.CodeValidationError)
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != code {
		t.Fatalf("want %s APIError, got %v", code, err)
	}
}

// --- service-level tests over fakes -----------------------------------------

type fakeOverviewStore struct {
	sessionIDs  []string
	activity    store.OverviewActivity
	hasSent     bool
	gotWindow   store.OverviewWindow
	gotHasSent  []string
	activityErr error
}

func (f *fakeOverviewStore) OrgSessionIDs(context.Context, string) ([]string, error) {
	return f.sessionIDs, nil
}
func (f *fakeOverviewStore) Activity(_ context.Context, w store.OverviewWindow) (store.OverviewActivity, error) {
	f.gotWindow = w
	if f.activityErr != nil {
		return store.OverviewActivity{}, f.activityErr
	}
	if f.activity.MessagesReceived == nil {
		n := len(w.Bounds)
		return store.OverviewActivity{
			MessagesReceived: make([]int64, n), MessagesSent: make([]int64, n), SendsFailed: make([]int64, n),
			WebhookDeliveries: make([]int64, n), WebhookFailures: make([]int64, n),
		}, nil
	}
	return f.activity, nil
}
func (f *fakeOverviewStore) HasSentMessage(_ context.Context, _ string, ids []string) (bool, error) {
	f.gotHasSent = ids
	return f.hasSent, nil
}

type fakeOverviewSessions map[string]domain.WASession

func (f fakeOverviewSessions) Get(_ context.Context, id string) (domain.WASession, error) {
	s, ok := f[id]
	if !ok {
		return domain.WASession{}, domain.ErrNotFound("session not found")
	}
	return s, nil
}

func TestOverviewService_DefaultsTotalsAndScope(t *testing.T) {
	repo := &fakeOverviewStore{
		sessionIDs: []string{"s1", "s2"},
		hasSent:    true,
		activity: store.OverviewActivity{
			MessagesReceived:  []int64{1, 0, 0, 0, 0, 2, 3},
			MessagesSent:      []int64{0, 0, 0, 0, 0, 0, 4},
			SendsFailed:       []int64{0, 1, 0, 0, 0, 0, 0},
			WebhookDeliveries: []int64{5, 5, 5, 5, 5, 5, 5},
			WebhookFailures:   []int64{0, 0, 0, 0, 0, 0, 1},
			ActiveChats:       9,
		},
	}
	svc := NewOverviewService(repo, fakeOverviewSessions{}, 30)
	now := time.Date(2025, 10, 16, 7, 33, 20, 0, time.UTC)
	svc.SetClock(func() time.Time { return now })

	got, err := svc.Get(context.Background(), "org_a", OverviewQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Range != "7d" || got.TZ != "UTC" || got.RetentionDays != 30 || !got.HasSentMessage {
		t.Fatalf("header = %+v", got)
	}
	if len(got.Buckets) != 7 || got.From != got.Buckets[0] || got.To != now.UnixMilli() {
		t.Fatalf("window from=%d to=%d buckets=%v", got.From, got.To, got.Buckets)
	}
	want := apitypes.OverviewTotals{MessagesReceived: 6, MessagesSent: 4, ActiveChats: 9, SendsFailed: 1, WebhookDeliveries: 35, WebhookFailures: 1}
	if got.Totals != want {
		t.Fatalf("totals = %+v, want %+v", got.Totals, want)
	}
	w := repo.gotWindow
	if w.OrganizationID != "org_a" || w.SessionID != "" || !reflect.DeepEqual(w.SessionIDs, []string{"s1", "s2"}) ||
		!reflect.DeepEqual(w.Bounds, got.Buckets) || w.ToMs != got.To {
		t.Fatalf("window = %+v", w)
	}
}

func TestOverviewService_SessionFilter(t *testing.T) {
	repo := &fakeOverviewStore{sessionIDs: []string{"s1", "s2"}}
	sessions := fakeOverviewSessions{
		"s2":    {ID: "s2", OrganizationID: "org_a"},
		"other": {ID: "other", OrganizationID: "org_b"},
	}
	svc := NewOverviewService(repo, sessions, 0)

	got, err := svc.Get(context.Background(), "org_a", OverviewQuery{Range: "24h", TZ: "Asia/Jakarta", SessionID: "s2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.TZ != "Asia/Jakarta" || len(got.Series.MessagesReceived) != 24 {
		t.Fatalf("tz=%s series=%d", got.TZ, len(got.Series.MessagesReceived))
	}
	if repo.gotWindow.SessionID != "s2" || !reflect.DeepEqual(repo.gotWindow.SessionIDs, []string{"s2"}) {
		t.Fatalf("window = %+v", repo.gotWindow)
	}
	// hasSentMessage stays organization-wide.
	if !reflect.DeepEqual(repo.gotHasSent, []string{"s1", "s2"}) {
		t.Fatalf("has-sent scope = %v", repo.gotHasSent)
	}

	_, err = svc.Get(context.Background(), "org_a", OverviewQuery{SessionID: "other"})
	assertCode(t, err, domain.CodeNotFound)
	_, err = svc.Get(context.Background(), "org_a", OverviewQuery{SessionID: "missing"})
	assertCode(t, err, domain.CodeNotFound)
}

func TestOverviewService_RejectsUnknownZoneAndRange(t *testing.T) {
	svc := NewOverviewService(&fakeOverviewStore{}, fakeOverviewSessions{}, 0)
	for _, q := range []OverviewQuery{
		{TZ: "Mars/Olympus_Mons"},
		{TZ: "Local"},
		{Range: "1y"},
	} {
		_, err := svc.Get(context.Background(), "org_a", q)
		assertCode(t, err, domain.CodeValidationError)
	}
}
