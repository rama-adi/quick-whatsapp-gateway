package domain

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

// TestNewULID_FormatAndUniqueness checks the stable wire format and practical uniqueness guarantee.
// It generates a batch, parses every value, and rejects duplicates so ID-format or entropy regressions surface early.
func TestNewULID_FormatAndUniqueness(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	var prev string
	for i := 0; i < n; i++ {
		id := NewULID()
		// A canonical ULID is 26 chars of Crockford base32.
		if len(id) != 26 {
			t.Fatalf("ULID length = %d, want 26 (%q)", len(id), id)
		}
		if strings.ToUpper(id) != id {
			t.Errorf("ULID should be upper-case Crockford base32, got %q", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate ULID generated: %q", id)
		}
		seen[id] = struct{}{}
		// Monotonic within (and across) a millisecond: each is >= the previous.
		if prev != "" && id < prev {
			t.Errorf("ULID not monotonic: %q < %q", id, prev)
		}
		prev = id
	}
}

// TestNewULID_ConcurrentSafe exercises the mutex protecting shared monotonic entropy.
// Many goroutines mint IDs simultaneously; every result must remain parseable and unique without racing the entropy source.
func TestNewULID_ConcurrentSafe(t *testing.T) {
	const goroutines = 50
	const per = 200
	var mu sync.Mutex
	seen := make(map[string]struct{}, goroutines*per)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]string, 0, per)
			for i := 0; i < per; i++ {
				local = append(local, NewULID())
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range local {
				if _, dup := seen[id]; dup {
					t.Errorf("duplicate ULID under concurrency: %q", id)
				}
				seen[id] = struct{}{}
			}
		}()
	}
	wg.Wait()
	if len(seen) != goroutines*per {
		t.Errorf("got %d unique ULIDs, want %d", len(seen), goroutines*per)
	}
}

// TestNewMessageIDAt_SortsByMessageTime pins the import id contract: the ULID
// time is the message's own timestamp, clamped to now for zero or future values.
func TestNewMessageIDAt_SortsByMessageTime(t *testing.T) {
	old := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli()
	id := NewMessageIDAt(old)
	if !strings.HasPrefix(id, PrefixMessage) {
		t.Fatalf("id %q lacks %q prefix", id, PrefixMessage)
	}
	parsed := ulid.MustParse(strings.TrimPrefix(id, PrefixMessage))
	if int64(parsed.Time()) != old {
		t.Fatalf("ulid time = %d, want %d", parsed.Time(), old)
	}
	if live := NewMessageID(); id >= live {
		t.Fatalf("imported id %q should sort before live id %q", id, live)
	}
	before := time.Now().UnixMilli()
	for _, ts := range []int64{0, -5, time.Now().Add(24 * time.Hour).UnixMilli()} {
		got := int64(ulid.MustParse(strings.TrimPrefix(NewMessageIDAt(ts), PrefixMessage)).Time())
		if got < before || got > time.Now().UnixMilli() {
			t.Fatalf("ts %d: ulid time %d not clamped to now", ts, got)
		}
	}
}

// TestMessageIDSequence_MonotonicWithinMillisecond keeps same-millisecond
// messages of one batch in feed order, even with live ids minted in between.
func TestMessageIDSequence_MonotonicWithinMillisecond(t *testing.T) {
	seq := NewMessageIDSequence()
	ts := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	prev := seq.Next(ts - 1)
	for i := 0; i < 100; i++ {
		_ = NewMessageIDAt(ts)
		_ = NewMessageID()
		id := seq.Next(ts)
		if id <= prev {
			t.Fatalf("sequence not monotonic: %q <= %q", id, prev)
		}
		prev = id
	}
}
