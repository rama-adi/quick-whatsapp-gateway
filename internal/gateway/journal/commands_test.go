package journal

import (
	"context"
	"testing"
	"time"
)

// TestCommandResultRoundTripAndImmutability verifies a definite outcome is
// stored once and a repeated save keeps the first terminal result.
func TestCommandResultRoundTripAndImmutability(t *testing.T) {
	ctx := context.Background()
	j, _ := openTestJournal(t, testConfig(1024))

	if record, err := j.LookupCommand(ctx, "cmd_1"); err != nil || record != nil {
		t.Fatalf("absent lookup = %#v, %v", record, err)
	}
	sentAt := time.UnixMilli(1234).UTC()
	if err := j.SaveCommandResult(ctx, CommandResult{
		CommandID: "cmd_1", SessionID: "ses_1", Status: CommandSent,
		WAMessageID: "WA_1", UpdatedAt: sentAt,
	}); err != nil {
		t.Fatalf("save sent: %v", err)
	}
	// A later attempt for the same command id must not overwrite the outcome.
	if err := j.SaveCommandResult(ctx, CommandResult{
		CommandID: "cmd_1", SessionID: "ses_1", Status: CommandFailed,
		Error: "second attempt failed", UpdatedAt: time.UnixMilli(9999).UTC(),
	}); err != nil {
		t.Fatalf("conflicting save: %v", err)
	}
	record, err := j.LookupCommand(ctx, "cmd_1")
	if err != nil || record == nil {
		t.Fatalf("lookup = %#v, %v", record, err)
	}
	if record.Status != CommandSent || record.WAMessageID != "WA_1" || !record.UpdatedAt.Equal(sentAt) {
		t.Fatalf("stored result mutated: %#v", record)
	}

	if err := j.SaveCommandResult(ctx, CommandResult{CommandID: "cmd_2", SessionID: "s", Status: "weird", UpdatedAt: sentAt}); err == nil {
		t.Fatal("unknown status accepted")
	}
}

// TestPruneCommandsRemovesOnlyExpiredResults pins the retention boundary.
func TestPruneCommandsRemovesOnlyExpiredResults(t *testing.T) {
	ctx := context.Background()
	j, _ := openTestJournal(t, testConfig(1024))

	old := time.Now().Add(-2 * CommandResultRetention).UTC()
	fresh := time.Now().UTC()
	for _, item := range []struct {
		id    string
		when  time.Time
		state string
	}{
		{"cmd_old", old, CommandSent},
		{"cmd_fresh", fresh, CommandFailed},
	} {
		if err := j.SaveCommandResult(ctx, CommandResult{
			CommandID: item.id, SessionID: "ses_1", Status: item.state, UpdatedAt: item.when,
		}); err != nil {
			t.Fatalf("save %s: %v", item.id, err)
		}
	}
	if err := j.PruneCommands(ctx, time.Now()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if record, _ := j.LookupCommand(ctx, "cmd_old"); record != nil {
		t.Fatalf("expired result survived: %#v", record)
	}
	if record, _ := j.LookupCommand(ctx, "cmd_fresh"); record == nil {
		t.Fatal("fresh result was pruned")
	}
}
