package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// pagedLogReader serves a fixed event log the way the MySQL reader does: an
// unknown cursor starts at the beginning and each call returns at most
// pageSize entries regardless of the requested limit.
type pagedLogReader struct {
	entries  []domain.EventLogEntry
	pageSize int
}

func (r *pagedLogReader) ListSince(_ context.Context, _, _, afterEventID string, limit int) ([]domain.EventLogEntry, error) {
	start := 0
	for i, e := range r.entries {
		if e.EventID == afterEventID {
			start = i + 1
			break
		}
	}
	n := min(limit, r.pageSize)
	end := min(start+n, len(r.entries))
	return r.entries[start:end], nil
}

func newLog(n int) []domain.EventLogEntry {
	out := make([]domain.EventLogEntry, n)
	for i := range out {
		out[i] = domain.EventLogEntry{
			EventID:        fmt.Sprintf("e%04d", i),
			OrganizationID: "org_1",
			SessionID:      "sess_1",
			Type:           domain.EventSessionStatus,
			Payload:        []byte(`{"status":"working"}`),
			CreatedAt:      int64(i),
		}
	}
	return out
}

// recordingSink collects frame event types and cancels the pump once the
// replay phase is over (the first frame after `want` data frames, or the
// data frame count itself when nothing follows).
type recordingSink struct {
	mu     sync.Mutex
	events []string
	data   int
	want   int
	cancel context.CancelFunc
}

func (s *recordingSink) Send(_ context.Context, payload []byte) error {
	var head struct {
		Event string `json:"event"`
		ID    string `json:"id"`
	}
	_ = json.Unmarshal(payload, &head)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, head.Event)
	if head.ID != "" {
		s.data++
	}
	if s.data >= s.want {
		s.cancel()
	}
	return nil
}

func runReplay(t *testing.T, n int, since string, want int) []string {
	t.Helper()
	_, rc := newMiniRedis(t)
	pump := NewPump(PumpConfig{Redis: rc, LogReader: &pagedLogReader{entries: newLog(n), pageSize: 200}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &recordingSink{want: want, cancel: cancel}
	if err := pump.Run(ctx, sink, Scope{Organization: "org_1"}, nil, since); err != nil {
		t.Fatalf("run: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]string(nil), sink.events...)
}

func TestPumpReplayReadsPastThePageSizeAndSignalsTruncation(t *testing.T) {
	events := runReplay(t, 1500, "e0000", resumeReplayLimit)
	if got := len(events); got != 1+resumeReplayLimit+1 {
		t.Fatalf("frames = %d, want connected + %d replayed + resync", got, resumeReplayLimit)
	}
	if events[0] != "connected" {
		t.Fatalf("first frame = %q, want connected", events[0])
	}
	if last := events[len(events)-1]; last != "resync" {
		t.Fatalf("last frame = %q, want resync", last)
	}
}

func TestPumpReplayWithinTheCapSendsNoResync(t *testing.T) {
	events := runReplay(t, 450, "e0000", 449)
	if got := len(events); got != 1+449 {
		t.Fatalf("frames = %d, want connected + 449 replayed", got)
	}
	for _, e := range events {
		if e == "resync" {
			t.Fatal("unexpected resync frame")
		}
	}
}

func TestPumpReplayExactlyAtTheCapSendsNoResync(t *testing.T) {
	events := runReplay(t, resumeReplayLimit+1, "e0000", resumeReplayLimit)
	if events[len(events)-1] == "resync" {
		t.Fatal("unexpected resync frame when the log ends at the cap")
	}
}
