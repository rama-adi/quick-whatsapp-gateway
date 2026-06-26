package stream

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// testRig bundles the handler with the redis machinery the test publishes through.
type testRig struct {
	h      *Handler
	ticker *fakeTicker
	mr     *miniredis.Miniredis
	rc     *redis.Client
}

// newTestHandler builds a Handler with the supplied organization, log reader, and a
// fake clock whose single ticker is exposed for manual heartbeat control.
func newTestHandler(t *testing.T, organization OrganizationAccessor, lr EventLogReader) testRig {
	t.Helper()
	mr, rc := newMiniRedis(t)
	tk := newFakeTicker()
	h := NewHandler(HandlerConfig{
		Redis:        rc,
		Organization: organization,
		LogReader:    lr,
		Clock:        &fakeClock{ticker: tk},
	})
	return testRig{h: h, ticker: tk, mr: mr, rc: rc}
}

// waitForExactSub blocks until at least one subscriber is registered on the exact
// channel, so a subsequent publish is not lost to a race with subscription setup.
func waitForExactSub(t *testing.T, mr *miniredis.Miniredis, channel string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(mr.PubSubChannels(channel)) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no subscriber appeared on channel %q", channel)
}

// waitForPatternSub blocks until at least one pattern subscriber is registered.
func waitForPatternSub(t *testing.T, mr *miniredis.Miniredis) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mr.PubSubNumPat() > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no pattern subscriber appeared")
}

func TestHandler_Unauthorized(t *testing.T) {
	rig := newTestHandler(t, staticOrganization(""), nil)
	resp, _, _ := startStream(t, rig.h, "?events=*")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestHandler_EmptyFilterRejected(t *testing.T) {
	rig := newTestHandler(t, staticOrganization("ten_a"), nil)
	resp, _, _ := startStream(t, rig.h, "?events=%20,%20") // events=" , "
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_NDJSONEncodingAndTail(t *testing.T) {
	rig := newTestHandler(t, staticOrganization("ten_a"), nil)

	resp, cancel, lines := startStream(t, rig.h, "?events=*")
	defer cancel()

	if got := resp.Header.Get("Content-Type"); got != "application/x-ndjson" {
		t.Fatalf("content-type = %q", got)
	}

	// Give the handler a moment to open its subscription before publishing.
	waitForPatternSub(t, rig.mr)

	e := domain.NewEvent(domain.EventMessage, "sess_1", "ten_a", map[string]any{"text": "hello"})
	publishEvent(t, rig.rc, e)

	got := recv(t, lines)
	if got["event"] != domain.EventMessage {
		t.Errorf("event = %v, want %s", got["event"], domain.EventMessage)
	}
	if got["id"] != e.ID {
		t.Errorf("id = %v, want %s", got["id"], e.ID)
	}
	if got["schema"] != domain.Schema {
		t.Errorf("schema = %v, want %s", got["schema"], domain.Schema)
	}
	payload, ok := got["payload"].(map[string]any)
	if !ok || payload["text"] != "hello" {
		t.Errorf("payload = %v", got["payload"])
	}
}

func TestHandler_EventTypeFiltering(t *testing.T) {
	rig := newTestHandler(t, staticOrganization("ten_a"), nil)

	// Only subscribe to message + poll.vote.
	_, cancel, lines := startStream(t, rig.h, "?events=message,poll.vote")
	defer cancel()
	waitForPatternSub(t, rig.mr)

	// This one must be filtered OUT.
	publishEvent(t, rig.rc, domain.NewEvent(domain.EventChatUpdate, "sess_1", "ten_a", nil))
	// This one must pass.
	want := domain.NewEvent(domain.EventPollVote, "sess_1", "ten_a", nil)
	publishEvent(t, rig.rc, want)

	got := recv(t, lines)
	if got["event"] != domain.EventPollVote {
		t.Fatalf("first delivered event = %v, want %s (chat.update should be filtered)", got["event"], domain.EventPollVote)
	}
	if got["id"] != want.ID {
		t.Errorf("id = %v, want %s", got["id"], want.ID)
	}
}

func TestHandler_SessionFilter(t *testing.T) {
	rig := newTestHandler(t, staticOrganization("ten_a"), nil)

	_, cancel, lines := startStream(t, rig.h, "?events=*&session=sess_1")
	defer cancel()
	waitForExactSub(t, rig.mr, sessionChannel("ten_a", "sess_1"))

	// Different session — must not be delivered (different channel entirely).
	publishEvent(t, rig.rc, domain.NewEvent(domain.EventMessage, "sess_OTHER", "ten_a", nil))
	want := domain.NewEvent(domain.EventMessage, "sess_1", "ten_a", nil)
	publishEvent(t, rig.rc, want)

	got := recv(t, lines)
	if got["session"] != "sess_1" || got["id"] != want.ID {
		t.Fatalf("delivered wrong event: %v", got)
	}
}

func TestHandler_Heartbeat(t *testing.T) {
	rig := newTestHandler(t, staticOrganization("ten_a"), nil)

	_, cancel, lines := startStream(t, rig.h, "?events=*")
	defer cancel()
	waitForPatternSub(t, rig.mr)

	// Drive a heartbeat tick manually.
	rig.ticker.tick()

	got := recv(t, lines)
	if got["event"] != "ping" {
		t.Fatalf("expected ping, got %v", got)
	}
	if _, ok := got["timestamp"]; !ok {
		t.Errorf("ping missing timestamp: %v", got)
	}
}

func TestHandler_SinceResumeThenTail(t *testing.T) {
	// Two persisted entries to replay, then a live one to tail.
	e1 := domain.EventLogEntry{EventID: "evt_1", OrganizationID: "ten_a", SessionID: "sess_1", Type: domain.EventMessage, Payload: json.RawMessage(`{"n":1}`), CreatedAt: 100}
	e2 := domain.EventLogEntry{EventID: "evt_2", OrganizationID: "ten_a", SessionID: "sess_1", Type: domain.EventChatUpdate, Payload: json.RawMessage(`{"n":2}`), CreatedAt: 200}
	lr := &fakeLogReader{entries: []domain.EventLogEntry{e1, e2}}

	rig := newTestHandler(t, staticOrganization("ten_a"), lr)

	_, cancel, lines := startStream(t, rig.h, "?events=*&since=evt_0")
	defer cancel()

	// Replay should come first, in order.
	got1 := recv(t, lines)
	if got1["id"] != "evt_1" {
		t.Fatalf("first replay id = %v, want evt_1", got1["id"])
	}
	if got1["schema"] != domain.Schema {
		t.Errorf("replay missing schema: %v", got1)
	}
	got2 := recv(t, lines)
	if got2["id"] != "evt_2" {
		t.Fatalf("second replay id = %v, want evt_2", got2["id"])
	}

	// ListSince must have been called with the resume cursor.
	if lr.gotAfter != "evt_0" || lr.gotOrganization != "ten_a" {
		t.Errorf("ListSince args: organization=%q after=%q", lr.gotOrganization, lr.gotAfter)
	}

	// Now a live event tails after replay.
	waitForPatternSub(t, rig.mr)
	live := domain.NewEvent(domain.EventMessage, "sess_1", "ten_a", map[string]any{"live": true})
	publishEvent(t, rig.rc, live)

	got3 := recv(t, lines)
	if got3["id"] != live.ID {
		t.Fatalf("live tail id = %v, want %s", got3["id"], live.ID)
	}
}

func TestHandler_SinceDedupBoundary(t *testing.T) {
	// The last replayed entry shares its id with the next live publish (the echo
	// of the same event). The live tail must drop that one duplicate.
	boundary := domain.NewEvent(domain.EventMessage, "sess_1", "ten_a", map[string]any{"k": "v"})
	entry := domain.EventLogEntry{
		EventID: boundary.ID, OrganizationID: "ten_a", SessionID: "sess_1",
		Type: domain.EventMessage, Payload: json.RawMessage(`{"k":"v"}`), CreatedAt: 100,
	}
	lr := &fakeLogReader{entries: []domain.EventLogEntry{entry}}

	rig := newTestHandler(t, staticOrganization("ten_a"), lr)

	_, cancel, lines := startStream(t, rig.h, "?events=*&since=evt_prev")
	defer cancel()

	// Replayed boundary line.
	got1 := recv(t, lines)
	if got1["id"] != boundary.ID {
		t.Fatalf("replay id = %v, want %s", got1["id"], boundary.ID)
	}

	waitForPatternSub(t, rig.mr)
	// Publish the duplicate (boundary echo) THEN a fresh event.
	publishEvent(t, rig.rc, boundary)
	next := domain.NewEvent(domain.EventMessage, "sess_1", "ten_a", map[string]any{"k": "v2"})
	publishEvent(t, rig.rc, next)

	// The duplicate must be skipped; we should receive `next`, not boundary again.
	got2 := recv(t, lines)
	if got2["id"] == boundary.ID {
		t.Fatalf("boundary duplicate was not deduped")
	}
	if got2["id"] != next.ID {
		t.Fatalf("expected next event %s, got %v", next.ID, got2["id"])
	}
}

func TestHandler_ReplayError(t *testing.T) {
	lr := &fakeLogReader{err: errors.New("db down")}
	rig := newTestHandler(t, staticOrganization("ten_a"), lr)

	_, cancel, lines := startStream(t, rig.h, "?events=*&since=evt_0")
	defer cancel()

	got := recv(t, lines)
	if got["event"] != "error" {
		t.Fatalf("expected error line on replay failure, got %v", got)
	}
}

func TestHandler_ClientCancelStopsStream(t *testing.T) {
	rig := newTestHandler(t, staticOrganization("ten_a"), nil)

	_, cancel, lines := startStream(t, rig.h, "?events=*")
	waitForPatternSub(t, rig.mr)

	// Cancel the request context -> handler must return -> body closes -> the
	// readLines goroutine closes the channel.
	cancel()

	select {
	case _, ok := <-lines:
		// We may receive a final buffered line or nothing; ultimately the channel
		// must close. Drain until closed.
		if ok {
			for range lines {
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not stop after client cancel")
	}
}
