// Package domain holds the core, shared, pure-data types for the gateway:
// table entities (§5 DDL), string-typed enums, the versioned event envelope
// (§9), the API error envelope and inbound send-request bodies (§11), plus a
// handful of tiny pure helpers (ULIDs, epoch-ms clock).
//
// It is the linchpin every other package imports. By design it contains NO
// business logic and NO dependencies on external services — only the standard
// library and github.com/oklog/ulid/v2 (for ID generation). Keeping it pure is
// what lets the parallel implementation packages agree on shapes without
// coupling to each other.
package domain
