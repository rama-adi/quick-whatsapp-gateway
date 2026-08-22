package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/events"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/inbound"
)

// This file holds the small impedance-matching adapters the composition root
// needs to plug the concrete store/stream/queue types into the consumer
// interfaces declared by internal/wa, internal/webhooks and internal/queue.
// They live here (a non-main package) so they are unit-testable and so cmd/gateway
// stays a thin wiring shim.

// ---------------------------------------------------------------------------
// wa.SessionRepo: the store.SessionRepo speaks value types + a 4-arg
// UpdateStatus; the manager's consumer interface speaks pointer types + a 3-arg
// UpdateStatus (stamping last_connected_at on WORKING). This adapter bridges the
// two without changing either side.
// ---------------------------------------------------------------------------

type publisher interface {
	Publish(ctx context.Context, e domain.Event) error
}

// EventSinkAdapter adapts a *stream.Publisher (Publish returning error) to the
// fire-and-forget wa.EventSink the manager expects. It is API-side only now:
// the gateway's manager publishes through its journal-backed sink.
type EventSinkAdapter struct {
	pub publisher
	log *slog.Logger
}

// NewEventSinkAdapter wraps a publisher for the wa.Manager. log may be nil.
func NewEventSinkAdapter(pub publisher, log *slog.Logger) *EventSinkAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &EventSinkAdapter{pub: pub, log: log}
}

func (a *EventSinkAdapter) Publish(ctx context.Context, evt domain.Event) {
	if err := a.pub.Publish(ctx, evt); err != nil {
		a.log.WarnContext(ctx, "event publish failed", "event_id", evt.ID, "type", evt.Type, "err", err)
	}
}

// ---------------------------------------------------------------------------
// wa.InboundHandler: wire the real inbound pipeline into the session manager.
// ---------------------------------------------------------------------------

// InboundPipelineHandler is the managers synchronous event callback into the
// ordered inbound pipeline. Processing errors are logged because whatsmeows
// callback has no retry acknowledgement channel; durable stage idempotency makes
// later protocol redelivery safe.
type InboundPipelineHandler struct {
	pipeline *inbound.Pipeline
	log      *slog.Logger
}

// NewInboundPipelineHandler wraps a pipeline without starting background work.
func NewInboundPipelineHandler(pipeline *inbound.Pipeline, log *slog.Logger) *InboundPipelineHandler {
	if log == nil {
		log = slog.Default()
	}
	return &InboundPipelineHandler{pipeline: pipeline, log: log}
}

func (h *InboundPipelineHandler) Handle(ctx context.Context, sessionID, organizationID string, isAdmin bool, evt any) {
	if h.pipeline == nil {
		return
	}
	if err := h.pipeline.Process(ctx, sessionID, organizationID, isAdmin, evt); err != nil {
		h.log.WarnContext(ctx, "inbound pipeline failed",
			"session", sessionID, "organization", organizationID, "type", typeName(evt), "err", err)
	}
}

// pollVoteDecryptor decrypts an incoming poll-vote event into the hex-encoded
// SHA-256 hashes of the selected options. Satisfied by wa.LiveOps.
type pollVoteDecryptor interface {
	DecryptPollVote(ctx context.Context, sessionID string, evt any) ([]string, error)
}

// pollOptionStore reads a poll's stored option list so vote hashes can be
// resolved to option text. Satisfied by *store.PollRepo.
type pollOptionStore interface {
	GetOptions(ctx context.Context, sessionID, pollMessageID string) ([]string, error)
}

// InboundNormalizer adapts events.Normalize to the inbound.Normalizer port and,
// for poll votes, enriches the result by decrypting the vote and resolving the
// selected option hashes to readable text (decryptor + polls are optional; when
// either is nil, votes are left with empty SelectedOptions).
type InboundNormalizer struct {
	decryptor pollVoteDecryptor
	polls     pollOptionStore
	log       *slog.Logger
}

type ownIDResolver interface {
	OwnIDs(ctx context.Context, sessionID string) (jid string, lid string)
}

// NewInboundNormalizer composes protocol normalization with optional poll-vote
// decryption and option resolution. Missing optional collaborators leave vote
// selections empty rather than rejecting unrelated inbound events.
func NewInboundNormalizer(decryptor pollVoteDecryptor, polls pollOptionStore) *InboundNormalizer {
	return &InboundNormalizer{decryptor: decryptor, polls: polls, log: slog.Default()}
}

var _ inbound.Normalizer = (*InboundNormalizer)(nil)

func (n *InboundNormalizer) Normalize(ctx context.Context, evt any, sessionID, organizationID string) (domain.Event, *inbound.NormalizedMessage, bool) {
	ev, pr, ok := events.Normalize(evt, sessionID, organizationID)
	if !ok {
		return domain.Event{}, nil, false
	}
	nm := inboundMessageFromPersistResult(pr, ev, sessionID, organizationID)
	if nm == nil {
		nm = &inbound.NormalizedMessage{
			Kind:           inbound.KindOther,
			SessionID:      sessionID,
			OrganizationID: organizationID,
		}
	}
	if resolver, ok := n.decryptor.(ownIDResolver); ok {
		nm.SelfJID, nm.SelfLID = resolver.OwnIDs(ctx, sessionID)
	}
	if nm.Kind == inbound.KindPollVote && nm.PollVote != nil {
		n.resolvePollVote(ctx, sessionID, evt, &ev, nm)
	}
	return ev, nm, true
}

// resolvePollVote decrypts the vote and resolves the selected option hashes to
// option text, writing the result onto both the persistence view (nm.PollVote)
// and the outbound envelope (ev.Payload). A failure is logged and left as an
// empty selection rather than dropping the vote — the row + event still record
// who voted on which poll.
func (n *InboundNormalizer) resolvePollVote(ctx context.Context, sessionID string, evt any, ev *domain.Event, nm *inbound.NormalizedMessage) {
	if n.decryptor == nil || n.polls == nil {
		return
	}
	hashes, err := n.decryptor.DecryptPollVote(ctx, sessionID, evt)
	if err != nil {
		n.log.WarnContext(ctx, "poll vote decrypt failed",
			slog.String("session", sessionID),
			slog.String("poll", nm.PollVote.PollMessageID),
			slog.Any("err", err))
		return
	}
	options, err := n.polls.GetOptions(ctx, sessionID, nm.PollVote.PollMessageID)
	if err != nil {
		n.log.WarnContext(ctx, "poll options lookup failed",
			slog.String("session", sessionID),
			slog.String("poll", nm.PollVote.PollMessageID),
			slog.Any("err", err))
		// Fall through: resolveSelectedOptions returns the raw hashes when options
		// are unknown, which is still more useful than nothing.
	}
	selected := resolveSelectedOptions(options, hashes)
	nm.PollVote.SelectedOptions = json.RawMessage(mustMarshalJSON(selected))
	if mp, ok := ev.Payload.(apitypes.MessagePayload); ok {
		mp.SelectedOptions = selected
		ev.Payload = mp
	}
	// Keep the persisted raw_json consistent with the enriched envelope.
	nm.RawJSON = eventPayloadJSON(*ev)
}

// resolveSelectedOptions maps each selected option hash (hex-encoded SHA-256 of
// the option text) back to its option string. An unmatched hash is kept verbatim
// so a vote for an option we never stored is still legible as its hash.
func resolveSelectedOptions(options, selectedHashes []string) []string {
	out := make([]string, 0, len(selectedHashes))
	byHash := make(map[string]string, len(options))
	for _, opt := range options {
		sum := sha256.Sum256([]byte(opt))
		byHash[hex.EncodeToString(sum[:])] = opt
	}
	for _, h := range selectedHashes {
		if name, ok := byHash[strings.ToLower(h)]; ok {
			out = append(out, name)
		} else {
			out = append(out, h)
		}
	}
	return out
}

type inboundWebhookEnqueuer interface {
	Enqueue(ctx context.Context, evt domain.Event) (int, error)
}

// InboundWebhookEnqueuerAdapter discards the concrete enqueuers created-count
// while preserving its error for fan-out aggregation. A nil enqueuer is an
// explicit no-webhooks configuration and succeeds without work.
type InboundWebhookEnqueuerAdapter struct {
	enqueuer inboundWebhookEnqueuer
}

// NewInboundWebhookEnqueuerAdapter wraps the concrete webhook scheduler for the
// inbound consumer interface.
func NewInboundWebhookEnqueuerAdapter(enqueuer inboundWebhookEnqueuer) *InboundWebhookEnqueuerAdapter {
	return &InboundWebhookEnqueuerAdapter{enqueuer: enqueuer}
}

var _ inbound.WebhookEnqueuer = (*InboundWebhookEnqueuerAdapter)(nil)

func (a *InboundWebhookEnqueuerAdapter) Enqueue(ctx context.Context, evt domain.Event) error {
	if a == nil || a.enqueuer == nil {
		return nil
	}
	_, err := a.enqueuer.Enqueue(ctx, evt)
	return err
}

func inboundMessageFromPersistResult(pr events.PersistResult, ev domain.Event, sessionID, organizationID string) *inbound.NormalizedMessage {
	switch pr.Kind {
	case events.PersistMessage:
		return inboundMessageFromEventsMessage(pr.Message, inbound.KindMessage, ev, sessionID, organizationID)
	case events.PersistMessageReaction:
		return inboundMessageFromEventsMessage(pr.Message, inbound.KindOther, ev, sessionID, organizationID)
	case events.PersistMessageEdit:
		nm := inboundMessageFromEventsMessage(pr.Message, inbound.KindEdit, ev, sessionID, organizationID)
		if nm != nil && pr.Message != nil {
			nm.WAMessageID = pr.Message.TargetMessageID
		}
		return nm
	case events.PersistMessageRevoke:
		nm := inboundMessageFromEventsMessage(pr.Message, inbound.KindRevoke, ev, sessionID, organizationID)
		if nm != nil && pr.Message != nil {
			nm.WAMessageID = pr.Message.TargetMessageID
		}
		return nm
	case events.PersistPollVote:
		nm := inboundMessageFromEventsMessage(pr.Message, inbound.KindPollVote, ev, sessionID, organizationID)
		if nm != nil && pr.Message != nil {
			voterKey := nm.SenderLID
			if voterKey == "" {
				voterKey = nm.SenderJID
			}
			nm.PollVote = &inbound.NormalizedPollVote{
				PollMessageID: pr.Message.PollVoteTargetID,
				VoterLID:      voterKey,
				// Filled by InboundNormalizer.resolvePollVote (decrypt + resolve);
				// default to an empty selection if resolution is unavailable.
				SelectedOptions: json.RawMessage("[]"),
				TimestampMs:     pr.Message.Timestamp,
			}
		}
		return nm
	case events.PersistMessageStatus:
		return &inbound.NormalizedMessage{
			Kind:           inbound.KindReceipt,
			SessionID:      sessionID,
			OrganizationID: organizationID,
			ChatJID:        pr.ChatJID,
			Receipt: &inbound.NormalizedReceipt{
				MessageIDs: pr.MessageIDs,
				Status:     pr.MessageStatus,
			},
			RawJSON: eventPayloadJSON(ev),
		}
	case events.PersistContactUpdate:
		// A push-name / contact event carries a display name for a JID but no
		// canonical LID. Surface it as a sender-less name hint so capture fills an
		// existing identity's missing name (push name preferred over saved name).
		name := pr.PushName
		if name == "" {
			name = pr.ContactName
		}
		return &inbound.NormalizedMessage{
			Kind:           inbound.KindOther,
			SessionID:      sessionID,
			OrganizationID: organizationID,
			ChatJID:        pr.ContactJID,
			SenderJID:      pr.ContactJID,
			PushName:       name,
			RawJSON:        eventPayloadJSON(ev),
		}
	default:
		return &inbound.NormalizedMessage{
			Kind:           inbound.KindOther,
			SessionID:      sessionID,
			OrganizationID: organizationID,
			ChatJID:        pr.ChatJID,
			RawJSON:        eventPayloadJSON(ev),
		}
	}
}

func inboundMessageFromEventsMessage(m *events.NormalizedMessage, kind inbound.MessageKind, ev domain.Event, sessionID, organizationID string) *inbound.NormalizedMessage {
	if m == nil {
		return nil
	}
	ct := chatTypeFromClass(m.ChatClass)
	senderLID, senderJID := splitSenderIDs(m.SenderLID, m.SenderJID)
	body := m.Body
	if body == "" {
		body = string(eventPayloadJSON(ev))
	}
	// The push name is the SENDER's display name. It's a fine chat name for a DM
	// (the peer), but for a group it would clobber the chat name with whoever sent
	// the last message. Groups get their name from whatsapp_groups.subject at read
	// time, so leave ChatName empty here to avoid polluting chats.name.
	chatName := m.PushName
	if ct == domain.ChatGroup {
		chatName = ""
	}
	nm := &inbound.NormalizedMessage{
		Kind:            kind,
		SessionID:       sessionID,
		OrganizationID:  organizationID,
		ChatJID:         m.ChatJID,
		ChatType:        ct,
		ChatName:        chatName,
		IsDM:            ct == domain.ChatDM,
		IsGroup:         ct == domain.ChatGroup,
		FromMe:          m.FromMe,
		SenderLID:       senderLID,
		SenderJID:       senderJID,
		SenderPhone:     phoneFromJID(senderJID),
		PushName:        m.PushName,
		WAMessageID:     m.WAMessageID,
		MsgType:         m.MessageType,
		Body:            body,
		QuotedMessageID: m.QuotedMessageID,
		Mentions:        m.Mentions,
		HasMedia:        m.HasMedia,
		MediaMeta:       mediaMetaFromEvents(m.MediaInfo),
		TimestampMs:     m.Timestamp,
		RawJSON:         eventPayloadJSON(ev),
	}
	if m.Poll != nil {
		nm.Poll = &inbound.NormalizedPoll{
			Name:            m.Poll.Name,
			Options:         m.Poll.Options,
			SelectableCount: m.Poll.SelectableCount,
			EndTime:         m.Poll.EndTime,
			HideVotes:       m.Poll.HideVotes,
		}
	}
	if nm.IsGroup {
		nm.Group = &inbound.NormalizedGroup{GroupJID: m.ChatJID}
		if senderLID != "" {
			// A message records the sender's MEMBERSHIP; the push name belongs to
			// the identity (captured separately). The per-group tag isn't carried on
			// a message, so leave it empty (backfill / group-info fills it).
			nm.Members = append(nm.Members, inbound.NormalizedMember{
				LID:  senderLID,
				JID:  senderJID,
				Role: domain.RoleMember,
			})
		}
	}
	return nm
}

func chatTypeFromClass(c events.ChatClass) domain.ChatType {
	switch c {
	case events.ChatClassGroup:
		return domain.ChatGroup
	case events.ChatClassNewsletter:
		return domain.ChatNewsletter
	case events.ChatClassBroadcast:
		return domain.ChatBroadcast
	case events.ChatClassStatus:
		return domain.ChatStatus
	default:
		return domain.ChatDM
	}
}

func mediaMetaFromEvents(m *events.MediaMeta) *domain.MediaMeta {
	if m == nil {
		return nil
	}
	return &domain.MediaMeta{Mimetype: m.Mimetype, Size: m.Size, Filename: m.Filename}
}

func splitSenderIDs(senderLID, senderJID string) (string, string) {
	if isLID(senderLID) {
		return senderLID, senderJID
	}
	if isLID(senderJID) {
		return senderJID, ""
	}
	return "", senderJID
}

func isLID(jid string) bool {
	return strings.HasSuffix(jid, "@lid")
}

func phoneFromJID(jid string) string {
	const suffix = "@s.whatsapp.net"
	if !strings.HasSuffix(jid, suffix) {
		return ""
	}
	return strings.TrimSuffix(jid, suffix)
}

func eventPayloadJSON(evt domain.Event) json.RawMessage {
	return json.RawMessage(mustMarshalJSON(evt.Payload))
}

func mustMarshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func int64Ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func typeName(v any) string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T", v)
}
