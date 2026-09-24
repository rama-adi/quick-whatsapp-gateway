package outbound

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fakes for the consumer interfaces.
// ---------------------------------------------------------------------------

// fakeWA records the last call per method and returns a canned (id, ts, err).
type fakeWA struct {
	mu        sync.Mutex
	calls     []string // method names, in order
	id        string
	ts        int64
	err       error
	lastText  string
	lastTo    string
	lastQuote QuoteInfo

	lastMediaType string
	lastMediaData []byte
	lastMimetype  string
}

func newFakeWA() *fakeWA { return &fakeWA{id: "WAMSG1", ts: 1719400000000} }

func (f *fakeWA) record(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
}

func (f *fakeWA) SendText(_ context.Context, to, text string, quote QuoteInfo, _ []string) (string, int64, error) {
	f.record("SendText")
	f.lastTo, f.lastText = to, text
	f.lastQuote = quote
	return f.id, f.ts, f.err
}
func (f *fakeWA) SendPoll(_ context.Context, _, _ string, _ []string, _ int, _ int64, _ bool) (string, int64, error) {
	f.record("SendPoll")
	return f.id, f.ts, f.err
}
func (f *fakeWA) SendLocation(_ context.Context, _ string, _, _ float64, _ string) (string, int64, error) {
	f.record("SendLocation")
	return f.id, f.ts, f.err
}
func (f *fakeWA) SendContact(_ context.Context, _, _, _, _ string) (string, int64, error) {
	f.record("SendContact")
	return f.id, f.ts, f.err
}
func (f *fakeWA) SendMedia(_ context.Context, _ string, mediaType string, data []byte, mimetype, _, _ string, quote QuoteInfo, _ []string) (string, int64, error) {
	f.record("SendMedia")
	f.mu.Lock()
	f.lastMediaType = mediaType
	f.lastMediaData = data
	f.lastMimetype = mimetype
	f.lastQuote = quote
	f.mu.Unlock()
	return f.id, f.ts, f.err
}

func (f *fakeWA) SendAlbum(_ context.Context, _ string, _ string, medias []AlbumMedia, _ QuoteInfo, _ []string) (string, int64, error) {
	f.record("SendAlbum")
	f.lastMediaData = nil
	for _, media := range medias {
		f.lastMediaData = append(f.lastMediaData, media.Data...)
	}
	return f.id, f.ts, f.err
}
func (f *fakeWA) React(_ context.Context, _, _, _, _ string) (string, int64, error) {
	f.record("React")
	return f.id, f.ts, f.err
}
func (f *fakeWA) Edit(_ context.Context, _, _, _ string) (string, int64, error) {
	f.record("Edit")
	return f.id, f.ts, f.err
}
func (f *fakeWA) Revoke(_ context.Context, _, _, _ string) (string, int64, error) {
	f.record("Revoke")
	return f.id, f.ts, f.err
}
func (f *fakeWA) Vote(_ context.Context, _, _, _ string, _ []string) (string, int64, error) {
	f.record("Vote")
	return f.id, f.ts, f.err
}
func (f *fakeWA) Forward(_ context.Context, _, _, _, _ string) (string, int64, error) {
	f.record("Forward")
	return f.id, f.ts, f.err
}

func (f *fakeWA) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakeOutbox is an in-memory OutboxRepo keyed by id, with an idempotency index.
type fakeOutbox struct {
	mu       sync.Mutex
	byID     map[string]*domain.OutboxEntry
	byIdem   map[string]string // "organization\x00key" -> id
	inserts  int
	insertFn func(e *domain.OutboxEntry) error // optional override
}

func newFakeOutbox() *fakeOutbox {
	return &fakeOutbox{byID: map[string]*domain.OutboxEntry{}, byIdem: map[string]string{}}
}

func idemKey(organization, key string) string { return organization + "\x00" + key }

func (f *fakeOutbox) Insert(_ context.Context, e *domain.OutboxEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertFn != nil {
		return f.insertFn(e)
	}
	f.inserts++
	if e.IdempotencyKey != nil {
		k := idemKey(e.OrganizationID, *e.IdempotencyKey)
		if _, dup := f.byIdem[k]; dup {
			return domain.ErrConflict("duplicate idempotency key")
		}
		f.byIdem[k] = e.ID
	}
	cp := *e
	f.byID[e.ID] = &cp
	return nil
}

func (f *fakeOutbox) GetByIdempotencyKey(_ context.Context, organizationID, key string) (*domain.OutboxEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byIdem[idemKey(organizationID, key)]
	if !ok {
		return nil, nil
	}
	cp := *f.byID[id]
	return &cp, nil
}

func (f *fakeOutbox) UpdateStatus(_ context.Context, id string, status domain.OutboxStatus, waMessageID, errMsg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.byID[id]
	if !ok {
		return errors.New("not found")
	}
	e.Status = status
	if waMessageID != "" {
		e.WAMessageID = &waMessageID
	}
	if errMsg != "" {
		e.Error = &errMsg
	}
	e.UpdatedAt += 1
	return nil
}

func (f *fakeOutbox) ClaimQueued(_ context.Context, sessionID string, limit int) ([]*domain.OutboxEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*domain.OutboxEntry
	for _, e := range f.byID {
		if e.SessionID == sessionID && e.Status == domain.OutboxQueued {
			e.Status = domain.OutboxSending
			cp := *e
			out = append(out, &cp)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// allowLimiter / denyLimiter are trivial RateLimiter fakes.
type allowLimiter struct{ calls int }

func (a *allowLimiter) Allow(context.Context, string, int, int) (bool, time.Duration, error) {
	a.calls++
	return true, 0, nil
}

type denyLimiter struct{}

func (denyLimiter) Allow(context.Context, string, int, int) (bool, time.Duration, error) {
	return false, 30 * time.Second, nil
}

type fixedClock struct{ ms int64 }

func (c fixedClock) NowMs() int64 { return c.ms }

func testSession() domain.WASession {
	return domain.WASession{ID: "sess_1", OrganizationID: "ten_1", RatePerMin: 20, RatePerHour: 200}
}

func (f *fakeWA) SendInteractive(_ context.Context, req domain.SendRequest, quote QuoteInfo) (string, int64, error) {
	f.record("SendInteractive")
	f.lastTo, f.lastText, f.lastQuote = req.To, req.Text, quote
	return f.id, f.ts, f.err
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestSend_AlbumSupportsMixedBase64AndURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("video"))
	}))
	defer srv.Close()

	wa := newFakeWA()
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{ms: 1000})
	_, err := s.Send(context.Background(), testSession(), domain.SendRequest{
		Type: domain.SendTypeAlbum, To: "a@s.whatsapp.net", Caption: "one caption",
		Medias: []domain.AlbumMediaPayload{{Data: "aW1hZ2U=", Mimetype: "image/jpeg"}, {Type: domain.SendTypeVideo, URL: srv.URL}},
	}, SendOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"SendAlbum"}, wa.calls)
	require.Equal(t, []byte("imagevideo"), wa.lastMediaData)
}

func TestSend_AlbumValidatesItemCountAndSources(t *testing.T) {
	cases := []domain.SendRequest{
		{Type: domain.SendTypeAlbum, To: "a@s.whatsapp.net", Medias: []domain.AlbumMediaPayload{{Data: "aA=="}}},
		{Type: domain.SendTypeAlbum, To: "a@s.whatsapp.net", Medias: []domain.AlbumMediaPayload{{Data: "aA=="}, {Data: "aA==", URL: "https://example.com/x"}}},
		{Type: domain.SendTypeAlbum, To: "a@s.whatsapp.net", Medias: []domain.AlbumMediaPayload{{Data: "aA=="}, {Type: domain.SendTypeAudio, Data: "aA=="}}},
	}
	for _, req := range cases {
		wa := newFakeWA()
		s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{})
		_, err := s.Send(context.Background(), testSession(), req, SendOptions{})
		require.Error(t, err)
		require.Zero(t, wa.callCount())
	}
}

// fakeRecorder captures the SentMessages handed to it, with an optional error.
type fakeRecorder struct {
	mu  sync.Mutex
	got []SentMessage
	err error
}

func (f *fakeRecorder) RecordSent(_ context.Context, m SentMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, m)
	return nil
}

func (f *fakeRecorder) records() []SentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SentMessage(nil), f.got...)
}

type fakeQuoteResolver struct {
	msg domain.Message
	err error
}

func (f fakeQuoteResolver) GetByWAID(context.Context, string, string) (domain.Message, error) {
	return f.msg, f.err
}

func TestPacing_CancellationStopsBeforeDispatch(t *testing.T) {
	wa := newFakeWA()
	outbox := newFakeOutbox()
	s := NewSender(wa, outbox, &allowLimiter{}, fixedClock{},
		WithPacing(time.Hour), withRand(func() float64 { return 1 }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Send(ctx, testSession(),
		domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"},
		SendOptions{IdempotencyKey: "cancelled"})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, wa.callCount())
	entry, lookupErr := outbox.GetByIdempotencyKey(context.Background(), testSession().OrganizationID, "cancelled")
	require.NoError(t, lookupErr)
	require.Equal(t, domain.OutboxFailed, entry.Status)
}
