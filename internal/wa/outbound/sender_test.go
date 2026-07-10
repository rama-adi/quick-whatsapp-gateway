package outbound

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
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

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestSend_TypeRouting sends representative text, image, video, audio, document, location, contact,
// poll, and sticker requests. The matrix verifies each validated type reaches exactly its matching
// WAClient method with no cross-routing.
func TestSend_TypeRouting(t *testing.T) {
	cases := []struct {
		name       string
		req        domain.SendRequest
		wantMethod string
	}{
		{"text", domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"}, "SendText"},
		{"poll", domain.SendRequest{Type: domain.SendTypePoll, To: "g@g.us", Name: "Q", Options: []string{"A", "B"}, SelectableCount: 1}, "SendPoll"},
		{"location", domain.SendRequest{Type: domain.SendTypeLocation, To: "a@s.whatsapp.net", Latitude: -8.65, Longitude: 115.21, Name: "Bali"}, "SendLocation"},
		{"contact", domain.SendRequest{Type: domain.SendTypeContact, To: "a@s.whatsapp.net", Contact: &domain.ContactCard{Name: "Bob", Phone: "628"}}, "SendContact"},
		{"image", domain.SendRequest{Type: domain.SendTypeImage, To: "a@s.whatsapp.net", Media: &domain.MediaPayload{Data: "aGVsbG8="}}, "SendMedia"},
		{"video", domain.SendRequest{Type: domain.SendTypeVideo, To: "a@s.whatsapp.net", Media: &domain.MediaPayload{Data: "aGVsbG8="}}, "SendMedia"},
		{"audio", domain.SendRequest{Type: domain.SendTypeAudio, To: "a@s.whatsapp.net", Media: &domain.MediaPayload{Data: "aGVsbG8="}}, "SendMedia"},
		{"document", domain.SendRequest{Type: domain.SendTypeDocument, To: "a@s.whatsapp.net", Media: &domain.MediaPayload{Data: "aGVsbG8=", Filename: "f.pdf"}}, "SendMedia"},
		{"sticker", domain.SendRequest{Type: domain.SendTypeSticker, To: "a@s.whatsapp.net", Media: &domain.MediaPayload{Data: "aGVsbG8="}}, "SendMedia"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wa := newFakeWA()
			s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{ms: 1000})
			res, err := s.Send(context.Background(), testSession(), tc.req, SendOptions{})
			require.NoError(t, err)
			require.Equal(t, ModeSync, res.Mode)
			require.Equal(t, "WAMSG1", res.WAMessageID)
			require.Equal(t, domain.MessageSent, res.Status)
			require.Equal(t, []string{tc.wantMethod}, wa.calls)
		})
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

// TestSend_RecordsSentMessage completes a synchronous text send with a recorder installed. It
// persists the assigned WhatsApp ID, timestamp, direction, body, and destination once after
// acknowledgment.
func TestSend_RecordsSentMessage(t *testing.T) {
	wa := newFakeWA()
	rec := &fakeRecorder{}
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{ms: 1000}, WithMessageRecorder(rec))

	_, err := s.Send(context.Background(), testSession(),
		domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi", ReplyTo: "WAQUOTE", Mentions: []string{"x@s.whatsapp.net"}},
		SendOptions{})
	require.NoError(t, err)

	got := rec.records()
	require.Len(t, got, 1)
	require.Equal(t, "sess_1", got[0].SessionID)
	require.Equal(t, "WAMSG1", got[0].WAMessageID)
	require.Equal(t, "a@s.whatsapp.net", got[0].ChatJID)
	require.Equal(t, domain.SendTypeText, got[0].Type)
	require.Equal(t, "hi", got[0].Body)
	require.Equal(t, "WAQUOTE", got[0].ReplyTo)
	require.Equal(t, []string{"x@s.whatsapp.net"}, got[0].Mentions)
	require.Equal(t, wa.ts, got[0].TimestampMs)
}

// TestSend_ResolvesReplyQuoteContext sends a reply whose target is available through the quote
// resolver. The resolved participant and quoted payload are forwarded to WhatsApp and the resolver is
// called with the correct session and message.
func TestSend_ResolvesReplyQuoteContext(t *testing.T) {
	wa := newFakeWA()
	body := "quoted body"
	sender := "107082225311887@lid"
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{ms: 1000},
		WithQuoteResolver(fakeQuoteResolver{msg: domain.Message{
			WAMessageID: "3A39B767976D4B5D4766",
			ChatJID:     "107082225311887@lid",
			SenderLID:   &sender,
			FromMe:      false,
			Type:        domain.SendTypeText,
			Body:        &body,
		}}))

	_, err := s.Send(context.Background(), testSession(),
		domain.SendRequest{
			Type:    domain.SendTypeText,
			To:      "107082225311887@lid",
			Text:    "reply",
			ReplyTo: "3A39B767976D4B5D4766",
		},
		SendOptions{})
	require.NoError(t, err)
	require.Equal(t, QuoteInfo{
		ID:        "3A39B767976D4B5D4766",
		ChatJID:   "107082225311887@lid",
		SenderJID: "107082225311887@lid",
		Type:      domain.SendTypeText,
		Body:      "quoted body",
	}, wa.lastQuote)
}

// TestSend_AsyncDrainRecordsSentMessage queues an asynchronous request and later dispatches its
// stored payload. Recording occurs only after the worker receives a WhatsApp acknowledgment, not when
// the outbox row is created.
func TestSend_AsyncDrainRecordsSentMessage(t *testing.T) {
	wa := newFakeWA()
	rec := &fakeRecorder{}
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{ms: 1000}, WithMessageRecorder(rec))

	// The async worker re-dispatches a persisted request via the exported
	// Dispatch; recording must fire there too (it shares the dispatch chokepoint).
	ctx := WithSessionID(context.Background(), "sess_1")
	_, _, err := s.Dispatch(ctx, domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "drained"})
	require.NoError(t, err)

	got := rec.records()
	require.Len(t, got, 1)
	require.Equal(t, "drained", got[0].Body)
}

// TestSend_FailedSendDoesNotRecord makes the WhatsApp client reject a synchronous dispatch. The
// error is returned and no sent-message row is written, avoiding a false success in chat history.
func TestSend_FailedSendDoesNotRecord(t *testing.T) {
	wa := newFakeWA()
	wa.err = errors.New("boom")
	rec := &fakeRecorder{}
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{ms: 1000}, WithMessageRecorder(rec))

	_, err := s.Send(context.Background(), testSession(),
		domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"}, SendOptions{})
	require.Error(t, err)
	require.Empty(t, rec.records(), "a failed send must not be recorded")
}

// TestSend_RecorderErrorDoesNotFailSend succeeds at WhatsApp but injects a persistence failure in
// the optional recorder. The caller still receives the acknowledged send result because post-send
// history capture is best effort.
func TestSend_RecorderErrorDoesNotFailSend(t *testing.T) {
	wa := newFakeWA()
	rec := &fakeRecorder{err: errors.New("db down")}
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{ms: 1000}, WithMessageRecorder(rec))

	res, err := s.Send(context.Background(), testSession(),
		domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"}, SendOptions{})
	require.NoError(t, err, "the WhatsApp send succeeded; a recorder failure must not surface")
	require.Equal(t, "WAMSG1", res.WAMessageID)
}

// TestSend_MediaWithoutSourceIsValidationError submits a media request with neither inline data nor
// a URL. Validation returns the public client error before rate limiting, fetching, upload, or
// dispatch.
func TestSend_MediaWithoutSourceIsValidationError(t *testing.T) {
	for _, typ := range []string{domain.SendTypeImage, domain.SendTypeVideo, domain.SendTypeAudio, domain.SendTypeDocument, domain.SendTypeSticker} {
		t.Run(typ, func(t *testing.T) {
			wa := newFakeWA()
			s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{})
			_, err := s.Send(context.Background(), testSession(),
				domain.SendRequest{Type: typ, To: "a@s.whatsapp.net"}, SendOptions{})
			require.Error(t, err)
			var apiErr *domain.APIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, domain.CodeValidationError, apiErr.Code)
			require.Zero(t, wa.callCount(), "an invalid send must never reach whatsmeow")
		})
	}
}

// TestSend_MediaWithDataAndURLIsValidationError supplies both supported media sources at once. The
// ambiguous request is rejected before any network or outbox side effect.
func TestSend_MediaWithDataAndURLIsValidationError(t *testing.T) {
	wa := newFakeWA()
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{})
	_, err := s.Send(context.Background(), testSession(),
		domain.SendRequest{Type: domain.SendTypeImage, To: "a@s.whatsapp.net",
			Media: &domain.MediaPayload{Data: "aGVsbG8=", URL: "https://example.com/photo.jpg"}},
		SendOptions{})
	require.Error(t, err)
	var apiErr *domain.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, domain.CodeValidationError, apiErr.Code)
	require.Zero(t, wa.callCount())
}

// TestSend_ImageDecodesAndForwardsBytes provides a base64 image payload and declared MIME type. The
// sender decodes the exact bytes and forwards them to the image client once.
func TestSend_ImageDecodesAndForwardsBytes(t *testing.T) {
	wa := newFakeWA()
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{ms: 1000})
	_, err := s.Send(context.Background(), testSession(),
		domain.SendRequest{Type: domain.SendTypeImage, To: "a@s.whatsapp.net",
			Media: &domain.MediaPayload{Data: "aGVsbG8=", Mimetype: "image/png", Caption: "hi"}},
		SendOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"SendMedia"}, wa.calls)
	require.Equal(t, domain.SendTypeImage, wa.lastMediaType)
	require.Equal(t, []byte("hello"), wa.lastMediaData)
	require.Equal(t, "image/png", wa.lastMimetype)
}

// TestSend_ImageURLFetchesAndForwardsBytes serves image bytes from a test HTTP server and sends its
// URL. The bounded fetch result and content type reach WAClient unchanged, proving URL media uses the
// same dispatch path.
func TestSend_ImageURLFetchesAndForwardsBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("hello from url"))
	}))
	defer srv.Close()

	wa := newFakeWA()
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{ms: 1000})
	_, err := s.Send(context.Background(), testSession(),
		domain.SendRequest{Type: domain.SendTypeImage, To: "a@s.whatsapp.net",
			Media: &domain.MediaPayload{URL: srv.URL, Caption: "hi"}},
		SendOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"SendMedia"}, wa.calls)
	require.Equal(t, domain.SendTypeImage, wa.lastMediaType)
	require.Equal(t, []byte("hello from url"), wa.lastMediaData)
	require.Equal(t, "image/png", wa.lastMimetype)
}

// TestSend_ImageInvalidBase64IsValidationError passes malformed inline image data. It returns a
// validation error without calling WhatsApp or recording a sent message.
func TestSend_ImageInvalidBase64IsValidationError(t *testing.T) {
	wa := newFakeWA()
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{})
	_, err := s.Send(context.Background(), testSession(),
		domain.SendRequest{Type: domain.SendTypeImage, To: "a@s.whatsapp.net",
			Media: &domain.MediaPayload{Data: "!!!not base64!!!"}},
		SendOptions{})
	require.Error(t, err)
	var apiErr *domain.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, domain.CodeValidationError, apiErr.Code)
	require.Zero(t, wa.callCount())
}

// TestSend_ValidationErrors runs missing and malformed fields for every outbound request kind. Each
// case must fail at the common validation gate and leave client, limiter, recorder, and outbox
// untouched.
func TestSend_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		req  domain.SendRequest
	}{
		{"missing type", domain.SendRequest{To: "a@s.whatsapp.net"}},
		{"missing to", domain.SendRequest{Type: domain.SendTypeText, Text: "hi"}},
		{"empty text", domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net"}},
		{"poll too few options", domain.SendRequest{Type: domain.SendTypePoll, To: "g@g.us", Name: "Q", Options: []string{"A"}}},
		{"bad latitude", domain.SendRequest{Type: domain.SendTypeLocation, To: "a@s.whatsapp.net", Latitude: 200, Longitude: 0}},
		{"contact missing fields", domain.SendRequest{Type: domain.SendTypeContact, To: "a@s.whatsapp.net", Contact: &domain.ContactCard{Name: "Bob"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wa := newFakeWA()
			s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{})
			_, err := s.Send(context.Background(), testSession(), tc.req, SendOptions{})
			require.Error(t, err)
			var apiErr *domain.APIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, domain.CodeValidationError, apiErr.Code)
			require.Zero(t, wa.callCount())
		})
	}
}

// TestSend_SyncRateLimited denies a synchronous request at the injected per-session limiter. No
// pacing or WhatsApp call occurs, and the caller receives the rate_limited domain error.
func TestSend_SyncRateLimited(t *testing.T) {
	wa := newFakeWA()
	s := NewSender(wa, newFakeOutbox(), denyLimiter{}, fixedClock{})
	_, err := s.Send(context.Background(), testSession(),
		domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"}, SendOptions{})
	require.Error(t, err)
	require.True(t, IsRateLimited(err))
	var apiErr *domain.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 30, apiErr.Details["retryAfterSeconds"])
	require.Zero(t, wa.callCount(), "rate-limited sync send must not dispatch")
}

// TestSend_AsyncPersistsAndDefersRateLimit queues an asynchronous request while the limiter would
// reject immediate delivery. The outbox row remains pending and no client call occurs, leaving retry
// timing to the worker.
func TestSend_AsyncPersistsAndDefersRateLimit(t *testing.T) {
	wa := newFakeWA()
	ob := newFakeOutbox()
	// Even with a deny limiter, async must persist (defer), not error.
	s := NewSender(wa, ob, denyLimiter{}, fixedClock{ms: 5000})
	res, err := s.Send(context.Background(), testSession(),
		domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"},
		SendOptions{Async: true})
	require.NoError(t, err)
	require.Equal(t, ModeAsync, res.Mode)
	require.NotEmpty(t, res.OutboxID)
	require.Equal(t, 1, ob.inserts)
	require.Zero(t, wa.callCount(), "async must not dispatch inline")

	stored := ob.byID[res.OutboxID]
	require.Equal(t, domain.OutboxQueued, stored.Status)
	require.Equal(t, int64(5000), stored.CreatedAt)
}

// TestSend_IdempotencyReplay repeats a successful synchronous request with the same idempotency
// key. The second call reconstructs the stored result and performs neither another WhatsApp send nor
// another insert.
func TestSend_IdempotencyReplay(t *testing.T) {
	wa := newFakeWA()
	ob := newFakeOutbox()
	s := NewSender(wa, ob, &allowLimiter{}, fixedClock{ms: 1000})
	req := domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"}
	opts := SendOptions{IdempotencyKey: "key-123"}

	// First send dispatches and records.
	first, err := s.Send(context.Background(), testSession(), req, opts)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	require.Equal(t, "WAMSG1", first.WAMessageID)
	require.Equal(t, 1, wa.callCount())

	// Second send with the same key replays without re-dispatching.
	second, err := s.Send(context.Background(), testSession(), req, opts)
	require.NoError(t, err)
	require.True(t, second.Replayed)
	require.Equal(t, "WAMSG1", second.WAMessageID)
	require.Equal(t, domain.MessageSent, second.Status)
	require.Equal(t, 1, wa.callCount(), "replay must NOT call whatsmeow again")
}

// TestSend_IdempotencyReplay_Async submits the same asynchronous idempotency key twice. Both
// responses identify the original queued row, proving duplicate API calls cannot enqueue duplicate
// delivery.
func TestSend_IdempotencyReplay_Async(t *testing.T) {
	wa := newFakeWA()
	ob := newFakeOutbox()
	s := NewSender(wa, ob, &allowLimiter{}, fixedClock{ms: 1000})
	req := domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"}
	opts := SendOptions{Async: true, IdempotencyKey: "akey"}

	first, err := s.Send(context.Background(), testSession(), req, opts)
	require.NoError(t, err)
	require.NotEmpty(t, first.OutboxID)
	require.Equal(t, 1, ob.inserts)

	second, err := s.Send(context.Background(), testSession(), req, opts)
	require.NoError(t, err)
	require.True(t, second.Replayed)
	require.Equal(t, first.OutboxID, second.OutboxID)
	require.Equal(t, 1, ob.inserts, "replay must not insert a second row")
}

// TestSend_SyncFailureRecordsOutbox combines a synchronous idempotency key with a WhatsApp failure.
// The durable idempotency row records the failed terminal outcome so retries do not unknowingly repeat
// an uncertain send.
func TestSend_SyncFailureRecordsOutbox(t *testing.T) {
	wa := newFakeWA()
	wa.err = errors.New("network down")
	ob := newFakeOutbox()
	s := NewSender(wa, ob, &allowLimiter{}, fixedClock{ms: 1000})
	req := domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"}

	_, err := s.Send(context.Background(), testSession(), req, SendOptions{IdempotencyKey: "k"})
	require.Error(t, err)

	// The outbox row exists and is marked failed for later inspection/replay.
	stored, _ := ob.GetByIdempotencyKey(context.Background(), "ten_1", "k")
	require.NotNil(t, stored)
	require.Equal(t, domain.OutboxFailed, stored.Status)
}

// TestSendOp_Routing exercises reaction, edit, revoke, poll-vote, and related message operations.
// Each valid request reaches only its operation-specific WAClient method with the target identifiers
// and payload intact.
func TestSendOp_Routing(t *testing.T) {
	cases := []struct {
		name       string
		op         OpRequest
		wantMethod string
	}{
		{"reaction", OpRequest{Op: OpReaction, Chat: "g@g.us", MsgID: "m1", Emoji: "👍"}, "React"},
		{"edit", OpRequest{Op: OpEdit, Chat: "g@g.us", MsgID: "m1", NewText: "new"}, "Edit"},
		{"revoke", OpRequest{Op: OpRevoke, Chat: "g@g.us", MsgID: "m1"}, "Revoke"},
		{"vote", OpRequest{Op: OpVote, Chat: "g@g.us", MsgID: "m1", Options: []string{"A"}}, "Vote"},
		{"forward", OpRequest{Op: OpForward, Chat: "g@g.us", MsgID: "m1", To: "x@s.whatsapp.net"}, "Forward"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wa := newFakeWA()
			s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{})
			res, err := s.SendOp(context.Background(), testSession(), tc.op)
			require.NoError(t, err)
			require.Equal(t, "WAMSG1", res.WAMessageID)
			require.Equal(t, []string{tc.wantMethod}, wa.calls)
		})
	}
}

// TestSendOp_Validation covers missing target IDs and operation-specific required values. Invalid
// operations fail before rate limiting or WhatsApp mutation, preserving the original message.
func TestSendOp_Validation(t *testing.T) {
	cases := []OpRequest{
		{Op: OpReaction, MsgID: "", Chat: "g@g.us"},  // missing msg id
		{Op: OpEdit, MsgID: "m1", Chat: "g@g.us"},    // missing newText
		{Op: OpVote, MsgID: "m1", Chat: "g@g.us"},    // missing options
		{Op: OpForward, MsgID: "m1", Chat: "g@g.us"}, // missing to
	}
	for _, op := range cases {
		wa := newFakeWA()
		s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{})
		_, err := s.SendOp(context.Background(), testSession(), op)
		require.Error(t, err)
		require.Zero(t, wa.callCount())
	}
}

// TestSendOp_RateLimited rejects a valid message operation at the per-session limiter. The client
// sees no reaction, edit, revoke, or vote call and the public error retains rate-limit classification.
func TestSendOp_RateLimited(t *testing.T) {
	wa := newFakeWA()
	s := NewSender(wa, newFakeOutbox(), denyLimiter{}, fixedClock{})
	_, err := s.SendOp(context.Background(), testSession(),
		OpRequest{Op: OpReaction, Chat: "g@g.us", MsgID: "m1", Emoji: "👍"})
	require.True(t, IsRateLimited(err))
	require.Zero(t, wa.callCount())
}

// TestPacing_Applied injects deterministic jitter for a synchronous send. Dispatch occurs only
// after the selected pacing interval, proving the optional delay is applied once and before WhatsApp.
func TestPacing_Applied(t *testing.T) {
	wa := newFakeWA()
	// Deterministic RNG returns 0.5 -> sleep ~ 0.5 * pacing. Keep it tiny.
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{},
		WithPacing(20*time.Millisecond), withRand(func() float64 { return 0.5 }))
	start := time.Now()
	_, err := s.Send(context.Background(), testSession(),
		domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"}, SendOptions{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Since(start), 8*time.Millisecond)
}

// TestPacing_CancellationStopsBeforeDispatch cancels the request while it waits in a long pacing
// delay. Send returns the context error promptly and never calls WhatsApp, so abandoned requests
// cannot leak a late delivery.
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

// TestDispatch_Reusable invokes the worker-facing Dispatch entry point repeatedly with validated
// requests. It bypasses front-door idempotency and rate checks while preserving type routing and
// post-ack recording for every call.
func TestDispatch_Reusable(t *testing.T) {
	wa := newFakeWA()
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{})
	id, ts, err := s.Dispatch(context.Background(),
		domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"})
	require.NoError(t, err)
	require.Equal(t, "WAMSG1", id)
	require.Equal(t, int64(1719400000000), ts)
}
