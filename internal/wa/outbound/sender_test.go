package outbound

import (
	"context"
	"errors"
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
	mu       sync.Mutex
	calls    []string // method names, in order
	id       string
	ts       int64
	err      error
	lastText string
	lastTo   string

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

func (f *fakeWA) SendText(_ context.Context, to, text, _ string, _ []string) (string, int64, error) {
	f.record("SendText")
	f.lastTo, f.lastText = to, text
	return f.id, f.ts, f.err
}
func (f *fakeWA) SendPoll(_ context.Context, _, _ string, _ []string, _ int) (string, int64, error) {
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
func (f *fakeWA) SendMedia(_ context.Context, _, mediaType string, data []byte, mimetype, _, _, _ string, _ []string) (string, int64, error) {
	f.record("SendMedia")
	f.mu.Lock()
	f.lastMediaType = mediaType
	f.lastMediaData = data
	f.lastMimetype = mimetype
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

func TestSend_RecorderErrorDoesNotFailSend(t *testing.T) {
	wa := newFakeWA()
	rec := &fakeRecorder{err: errors.New("db down")}
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{ms: 1000}, WithMessageRecorder(rec))

	res, err := s.Send(context.Background(), testSession(),
		domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"}, SendOptions{})
	require.NoError(t, err, "the WhatsApp send succeeded; a recorder failure must not surface")
	require.Equal(t, "WAMSG1", res.WAMessageID)
}

func TestSend_MediaWithoutDataIsValidationError(t *testing.T) {
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

func TestSendOp_RateLimited(t *testing.T) {
	wa := newFakeWA()
	s := NewSender(wa, newFakeOutbox(), denyLimiter{}, fixedClock{})
	_, err := s.SendOp(context.Background(), testSession(),
		OpRequest{Op: OpReaction, Chat: "g@g.us", MsgID: "m1", Emoji: "👍"})
	require.True(t, IsRateLimited(err))
	require.Zero(t, wa.callCount())
}

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

func TestDispatch_Reusable(t *testing.T) {
	wa := newFakeWA()
	s := NewSender(wa, newFakeOutbox(), &allowLimiter{}, fixedClock{})
	id, ts, err := s.Dispatch(context.Background(),
		domain.SendRequest{Type: domain.SendTypeText, To: "a@s.whatsapp.net", Text: "hi"})
	require.NoError(t, err)
	require.Equal(t, "WAMSG1", id)
	require.Equal(t, int64(1719400000000), ts)
}
