package inbound

import (
	"context"
	"sync"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// callOrder records the global sequence of side-effects across fakes so tests
// can assert ordering (e.g. auto-read before fan-out). It is shared by all fakes
// built from newFakes.
type callOrder struct {
	mu    sync.Mutex
	steps []string
}

func (c *callOrder) record(step string) {
	c.mu.Lock()
	c.steps = append(c.steps, step)
	c.mu.Unlock()
}

func (c *callOrder) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.steps))
	copy(out, c.steps)
	return out
}

// indexOf returns the position of the first step equal to s, or -1.
func indexOf(steps []string, s string) int {
	for i, v := range steps {
		if v == s {
			return i
		}
	}
	return -1
}

// --- Normalizer fake ---

type fakeNormalizer struct {
	evt domain.Event
	nm  *NormalizedMessage
	ok  bool
}

func (f *fakeNormalizer) Normalize(ctx context.Context, evt any, sessionID, organizationID string) (domain.Event, *NormalizedMessage, bool) {
	return f.evt, f.nm, f.ok
}

// --- CommandRegistry fake ---

type fakeCommands struct {
	order   *callOrder
	handled bool
	err     error
	calls   []string // bodies handed to Handle
}

func (f *fakeCommands) Handle(ctx context.Context, sessionID, body string) (bool, error) {
	f.order.record("command")
	f.calls = append(f.calls, body)
	return f.handled, f.err
}

type fakeLoginInterceptor struct {
	handled bool
	err     error
	calls   int
}

func (f *fakeLoginInterceptor) HandleLogin(ctx context.Context, nm *NormalizedMessage) (bool, error) {
	f.calls++
	return f.handled, f.err
}

// --- Repos fake ---

type fakeRepos struct {
	order *callOrder

	identities     []IdentityUpsert
	nameFills      []IdentityNameFill
	groups         []GroupUpsert
	members        []GroupMemberUpsert
	mentionDetails map[string]MentionDetail
	quotedCtx      map[string]QuotedContext // keyed by quoted wa_message_id
	chats          []ChatUpsert
	messages       []MessageInsert
	edited         []string
	deleted        []string
	statusUpd      []MessageStatusUpdate
	polls          []PollUpsert
	pollVotes      []PollVoteInsert
	eventLog       []domain.Event
	failOn         string // method name to fail, e.g. "InsertMessage"
	failErr        error
}

func (r *fakeRepos) maybeFail(method string) error {
	if r.failOn == method {
		if r.failErr != nil {
			return r.failErr
		}
		return context.DeadlineExceeded
	}
	return nil
}

func (r *fakeRepos) UpsertIdentity(ctx context.Context, in IdentityUpsert) error {
	r.order.record("UpsertIdentity")
	if err := r.maybeFail("UpsertIdentity"); err != nil {
		return err
	}
	r.identities = append(r.identities, in)
	return nil
}

func (r *fakeRepos) FillIdentityName(ctx context.Context, in IdentityNameFill) error {
	r.order.record("FillIdentityName")
	if err := r.maybeFail("FillIdentityName"); err != nil {
		return err
	}
	r.nameFills = append(r.nameFills, in)
	return nil
}

func (r *fakeRepos) UpsertGroup(ctx context.Context, in GroupUpsert) error {
	r.order.record("UpsertGroup")
	if err := r.maybeFail("UpsertGroup"); err != nil {
		return err
	}
	r.groups = append(r.groups, in)
	return nil
}

func (r *fakeRepos) UpsertGroupMember(ctx context.Context, in GroupMemberUpsert) error {
	r.order.record("UpsertGroupMember")
	if err := r.maybeFail("UpsertGroupMember"); err != nil {
		return err
	}
	r.members = append(r.members, in)
	return nil
}

func (r *fakeRepos) ResolveMentionDetails(ctx context.Context, sessionID, groupJID string, mentions []string) (map[string]MentionDetail, error) {
	r.order.record("ResolveMentionDetails")
	if err := r.maybeFail("ResolveMentionDetails"); err != nil {
		return nil, err
	}
	out := make(map[string]MentionDetail, len(mentions))
	for _, jid := range mentions {
		out[jid] = r.mentionDetails[jid]
	}
	return out, nil
}

func (r *fakeRepos) LookupQuotedContext(ctx context.Context, sessionID, quotedMessageID string) (QuotedContext, bool, error) {
	r.order.record("LookupQuotedContext")
	if err := r.maybeFail("LookupQuotedContext"); err != nil {
		return QuotedContext{}, false, err
	}
	qc, ok := r.quotedCtx[quotedMessageID]
	return qc, ok, nil
}

func (r *fakeRepos) UpsertChat(ctx context.Context, in ChatUpsert) error {
	r.order.record("UpsertChat")
	if err := r.maybeFail("UpsertChat"); err != nil {
		return err
	}
	r.chats = append(r.chats, in)
	return nil
}

func (r *fakeRepos) InsertMessage(ctx context.Context, in MessageInsert) error {
	r.order.record("InsertMessage")
	if err := r.maybeFail("InsertMessage"); err != nil {
		return err
	}
	r.messages = append(r.messages, in)
	return nil
}

func (r *fakeRepos) MarkMessageEdited(ctx context.Context, sessionID, waMessageID, newBody string) error {
	r.order.record("MarkMessageEdited")
	if err := r.maybeFail("MarkMessageEdited"); err != nil {
		return err
	}
	r.edited = append(r.edited, waMessageID)
	return nil
}

func (r *fakeRepos) MarkMessageDeleted(ctx context.Context, sessionID, waMessageID string) error {
	r.order.record("MarkMessageDeleted")
	if err := r.maybeFail("MarkMessageDeleted"); err != nil {
		return err
	}
	r.deleted = append(r.deleted, waMessageID)
	return nil
}

func (r *fakeRepos) UpdateMessageStatus(ctx context.Context, in MessageStatusUpdate) error {
	r.order.record("UpdateMessageStatus")
	if err := r.maybeFail("UpdateMessageStatus"); err != nil {
		return err
	}
	r.statusUpd = append(r.statusUpd, in)
	return nil
}

func (r *fakeRepos) UpsertPoll(ctx context.Context, in PollUpsert) error {
	r.order.record("UpsertPoll")
	if err := r.maybeFail("UpsertPoll"); err != nil {
		return err
	}
	r.polls = append(r.polls, in)
	return nil
}

func (r *fakeRepos) InsertPollVote(ctx context.Context, in PollVoteInsert) error {
	r.order.record("InsertPollVote")
	if err := r.maybeFail("InsertPollVote"); err != nil {
		return err
	}
	r.pollVotes = append(r.pollVotes, in)
	return nil
}

func (r *fakeRepos) AppendEventLog(ctx context.Context, evt domain.Event) error {
	r.order.record("AppendEventLog")
	if err := r.maybeFail("AppendEventLog"); err != nil {
		return err
	}
	r.eventLog = append(r.eventLog, evt)
	return nil
}

// --- EventSink fake ---

type fakeSink struct {
	order     *callOrder
	published []domain.Event
	err       error
}

func (f *fakeSink) Publish(ctx context.Context, evt domain.Event) error {
	f.order.record("Publish")
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, evt)
	return nil
}

// --- WebhookEnqueuer fake ---

type fakeWebhooks struct {
	order    *callOrder
	enqueued []domain.Event
	err      error
}

func (f *fakeWebhooks) Enqueue(ctx context.Context, evt domain.Event) error {
	f.order.record("Enqueue")
	if f.err != nil {
		return f.err
	}
	f.enqueued = append(f.enqueued, evt)
	return nil
}

// --- WAClient fake ---

type readReceiptCall struct {
	sessionID, chatJID, senderJID string
	messageIDs                    []string
}

type fakeWA struct {
	order        *callOrder
	readReceipts []readReceiptCall
	presence     []string // states sent
	readErr      error
}

func (f *fakeWA) SendReadReceipt(ctx context.Context, sessionID, chatJID, senderJID string, messageIDs []string) error {
	f.order.record("SendReadReceipt")
	if f.readErr != nil {
		return f.readErr
	}
	f.readReceipts = append(f.readReceipts, readReceiptCall{sessionID, chatJID, senderJID, messageIDs})
	return nil
}

func (f *fakeWA) SendPresence(ctx context.Context, sessionID, chatJID, state string) error {
	f.order.record("SendPresence")
	f.presence = append(f.presence, state)
	return nil
}

// --- Clock fake ---

type fakeClock struct{ ms int64 }

func (c fakeClock) NowMs() int64 { return c.ms }

// --- harness ---

// fakes bundles every fake so a test can assert against them.
type fakes struct {
	order    *callOrder
	norm     *fakeNormalizer
	commands *fakeCommands
	repos    *fakeRepos
	sink     *fakeSink
	webhooks *fakeWebhooks
	wa       *fakeWA
	clock    fakeClock
}

func newFakes() *fakes {
	order := &callOrder{}
	return &fakes{
		order:    order,
		norm:     &fakeNormalizer{ok: true},
		commands: &fakeCommands{order: order},
		repos:    &fakeRepos{order: order},
		sink:     &fakeSink{order: order},
		webhooks: &fakeWebhooks{order: order},
		wa:       &fakeWA{order: order},
		clock:    fakeClock{ms: 1_700_000_000_000},
	}
}

// newPipeline builds a Pipeline wired to the fakes with the given options.
func (f *fakes) newPipeline(opts ...Option) *Pipeline {
	return NewPipeline(f.norm, f.commands, f.repos, f.sink, f.webhooks, f.wa, f.clock, opts...)
}
