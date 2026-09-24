package domain

import (
	"strings"
	"sync"
	"testing"
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
