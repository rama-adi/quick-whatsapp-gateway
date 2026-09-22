// Package events normalizes raw whatsmeow events into the versioned domain event
// catalog (§9) and applies source-level ignore rules (§7).
//
// Normalize translates a whatsmeow event into (a) a wire-safe domain.Event
// envelope whose payload is a camelCase struct that NEVER contains a raw
// protobuf, and (b) a PersistResult — the structured, protobuf-free handoff the
// inbound pipeline consumes for capture/persist/fan-out. IgnoreRules classifies a
// chat JID by its server to skip status/groups/channels/broadcast as configured.
//
// The package depends only on the stdlib, whatsmeow, and internal/domain; all
// collaborators (e.g. config) are consumer-defined here.
package events
