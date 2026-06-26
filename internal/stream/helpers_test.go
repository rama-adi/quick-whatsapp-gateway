package stream

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// fakeTicker is a manually-driven Ticker for deterministic heartbeat tests.
type fakeTicker struct {
	ch      chan time.Time
	stopped bool
	mu      sync.Mutex
}

func newFakeTicker() *fakeTicker { return &fakeTicker{ch: make(chan time.Time, 1)} }

func (f *fakeTicker) C() <-chan time.Time { return f.ch }
func (f *fakeTicker) Stop() {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
}

// tick delivers one heartbeat tick.
func (f *fakeTicker) tick() { f.ch <- time.Now() }

// fakeClock hands out a single pre-created fakeTicker.
type fakeClock struct{ ticker *fakeTicker }

func (c *fakeClock) NewTicker(time.Duration) Ticker { return c.ticker }

// fakeLogReader is an injectable EventLogReader returning canned entries.
type fakeLogReader struct {
	entries []domain.EventLogEntry
	err     error
	// captured args from the last ListSince call
	gotOrganization, gotSession, gotAfter string
	gotLimit                              int
}

func (r *fakeLogReader) ListSince(_ context.Context, organization, session, after string, limit int) ([]domain.EventLogEntry, error) {
	r.gotOrganization, r.gotSession, r.gotAfter, r.gotLimit = organization, session, after, limit
	if r.err != nil {
		return nil, r.err
	}
	return r.entries, nil
}

// staticOrganization always reports the same organization id.
func staticOrganization(id string) OrganizationAccessor {
	return OrganizationAccessorFunc(func(context.Context) (string, bool) {
		if id == "" {
			return "", false
		}
		return id, true
	})
}

// newMiniRedis spins up an in-memory Redis and a connected *redis.Client (which
// satisfies the RedisClient consumer interface) for the test, registering cleanup.
func newMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rc.Close()
		mr.Close()
	})
	return mr, rc
}

// readLines reads NDJSON lines from r into a channel, one decoded object per line.
// It closes the channel when the body ends (stream closed / client cancelled).
func readLines(r *http.Response) <-chan map[string]any {
	out := make(chan map[string]any)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(r.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal(line, &obj); err != nil {
				// Surface parse failures to the test as a sentinel line.
				out <- map[string]any{"__parse_error__": string(line)}
				continue
			}
			out <- obj
		}
	}()
	return out
}

// startStream wires a Handler and starts an in-process request against it using a
// real http server connection (so streaming/flushing behaves like production).
// It returns the response, a cancel func to simulate client disconnect, and the
// decoded-line channel.
func startStream(t *testing.T, h *Handler, query string) (*http.Response, context.CancelFunc, <-chan map[string]any) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events"+query, nil)
	if err != nil {
		cancel()
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp, cancel, readLines(resp)
}

// recv waits for one line or fails after a timeout.
func recv(t *testing.T, ch <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case obj, ok := <-ch:
		if !ok {
			t.Fatal("stream closed unexpectedly")
		}
		return obj
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream line")
		return nil
	}
}

// publishEvent marshals and publishes an event to its channel via the client,
// then waits briefly so miniredis delivers it before the test continues.
func publishEvent(t *testing.T, rc *redis.Client, e domain.Event) {
	t.Helper()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := rc.Publish(context.Background(), channelFor(e.Organization, e.Session), data).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}
}
