// Package waadapter connects the WhatsApp runtime to gateway-local pipelines.
// It intentionally has no API service, MySQL, or Redis dependencies.
package waadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/events"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/wa/inbound"
)

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

func (n *InboundNormalizer) Normalize(
	ctx context.Context,
	evt any,
	sessionID, organizationID string,
) (domain.Event, *inbound.NormalizedMessage, bool) {
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
func (n *InboundNormalizer) resolvePollVote(
	ctx context.Context,
	sessionID string,
	evt any,
	ev *domain.Event,
	nm *inbound.NormalizedMessage,
) {
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
		name, ok := byHash[strings.ToLower(h)]
		if !ok {
			name = h
		}
		out = append(out, name)
	}
	return out
}

func inboundMessageFromPersistResult(
	pr events.PersistResult,
	ev domain.Event,
	sessionID, organizationID string,
) *inbound.NormalizedMessage {
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

func inboundMessageFromEventsMessage(
	m *events.NormalizedMessage,
	kind inbound.MessageKind,
	ev domain.Event,
	sessionID, organizationID string,
) *inbound.NormalizedMessage {
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
	// Device-qualified JIDs carry a :device suffix; it is not part of the phone.
	phone, _, _ := strings.Cut(strings.TrimSuffix(jid, suffix), ":")
	return phone
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

func typeName(v any) string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T", v)
}
