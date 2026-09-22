package inbound

import (
	"context"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

// NoopRepos is a fully inert Repos implementation. The gateway owns no MySQL
// app-data tables any more: chats, messages, polls, votes, receipts, identities,
// and groups are projected API-side from committed events. Pipeline stages call
// these methods and skip persistence; lookups report "not found" so enrichment
// falls back to protocol-frame data.
//
// AppendEventLog is intentionally a no-op too: event_log is API-owned, written
// by the control ingest transaction.
type NoopRepos struct{}

var _ Repos = NoopRepos{}

func (NoopRepos) UpsertIdentity(context.Context, IdentityUpsert) error       { return nil }
func (NoopRepos) FillIdentityName(context.Context, IdentityNameFill) error   { return nil }
func (NoopRepos) UpsertGroup(context.Context, GroupUpsert) error             { return nil }
func (NoopRepos) UpsertGroupMember(context.Context, GroupMemberUpsert) error { return nil }

func (NoopRepos) ResolveMentionDetails(context.Context, string, string, []string) (map[string]MentionDetail, error) {
	return map[string]MentionDetail{}, nil
}

func (NoopRepos) LookupQuotedContext(context.Context, string, string) (QuotedContext, bool, error) {
	return QuotedContext{}, false, nil
}

func (NoopRepos) UpsertChat(context.Context, ChatUpsert) error                    { return nil }
func (NoopRepos) InsertMessage(context.Context, MessageInsert) error              { return nil }
func (NoopRepos) MarkMessageEdited(context.Context, string, string, string) error { return nil }
func (NoopRepos) MarkMessageDeleted(context.Context, string, string) error        { return nil }
func (NoopRepos) UpdateMessageStatus(context.Context, MessageStatusUpdate) error  { return nil }
func (NoopRepos) UpsertPoll(context.Context, PollUpsert) error                    { return nil }
func (NoopRepos) InsertPollVote(context.Context, PollVoteInsert) error            { return nil }
func (NoopRepos) AppendEventLog(context.Context, domain.Event) error              { return nil }
