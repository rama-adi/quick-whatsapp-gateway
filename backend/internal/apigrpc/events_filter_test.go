package apigrpc

import "testing"

// TestEventTypeFilterExcludesBulkFromWildcard keeps the gRPC stream in line
// with the WebSocket filter: "*" or an empty list never admits history.batch.
func TestEventTypeFilterExcludesBulkFromWildcard(t *testing.T) {
	for _, types := range [][]string{nil, {"*"}} {
		f := parseEventTypes(types)
		if !f.allows("history.sync") || f.allows("history.batch") {
			t.Fatalf("filter %v: history.sync=%v history.batch=%v", types, f.allows("history.sync"), f.allows("history.batch"))
		}
	}
	if !parseEventTypes([]string{"history.batch"}).allows("history.batch") {
		t.Fatal("explicit history.batch subscription must match")
	}
}
