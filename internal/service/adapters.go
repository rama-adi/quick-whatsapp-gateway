package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/events"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/inbound"
)

// This file holds the small impedance-matching adapters the composition root
// needs to plug the concrete store/stream/queue types into the consumer
// interfaces declared by internal/wa, internal/webhooks and internal/queue.
// They live here (a non-main package) so they are unit-testable and so cmd/server
// stays a thin wiring shim.

// ---------------------------------------------------------------------------
// wa.SessionRepo: the store.SessionRepo speaks value types + a 4-arg
// UpdateStatus; the manager's consumer interface speaks pointer types + a 3-arg
// UpdateStatus (stamping last_connected_at on WORKING). This adapter bridges the
// two without changing either side.
// ---------------------------------------------------------------------------

// ManagerSessionRepo adapts *store.SessionRepo to wa.SessionRepo.
type ManagerSessionRepo struct {
	repo  *store.SessionRepo
	clock func() int64
}

// NewManagerSessionRepo wraps a store.SessionRepo for the wa.Manager. clock may
// be nil (domain.NowMs is used).
func NewManagerSessionRepo(repo *store.SessionRepo, clock func() int64) *ManagerSessionRepo {
	if clock == nil {
		clock = domain.NowMs
	}
	return &ManagerSessionRepo{repo: repo, clock: clock}
}

var _ wa.SessionRepo = (*ManagerSessionRepo)(nil)

func (a *ManagerSessionRepo) Get(ctx context.Context, id string) (*domain.WASession, error) {
	s, err := a.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (a *ManagerSessionRepo) GetByJID(ctx context.Context, jid string) (*domain.WASession, error) {
	s, err := a.repo.GetByJID(ctx, jid)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (a *ManagerSessionRepo) ListByOrg(ctx context.Context, organizationID string) ([]*domain.WASession, error) {
	rows, err := a.repo.ListByOrg(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.WASession, len(rows))
	for i := range rows {
		s := rows[i]
		out[i] = &s
	}
	return out, nil
}

func (a *ManagerSessionRepo) Create(ctx context.Context, s *domain.WASession) error {
	return a.repo.Create(ctx, *s)
}

func (a *ManagerSessionRepo) Update(ctx context.Context, s *domain.WASession) error {
	s.UpdatedAt = a.clock()
	return a.repo.Update(ctx, *s)
}

func (a *ManagerSessionRepo) UpdateStatus(ctx context.Context, id string, status domain.SessionStatus) error {
	now := a.clock()
	if err := a.repo.UpdateStatus(ctx, id, status, now); err != nil {
		return err
	}
	// Stamp last_connected_at when a session reaches WORKING. Best-effort: load,
	// set, write through the full Update. A failure here is logged by the caller.
	if status == domain.SessionWorking {
		s, err := a.repo.Get(ctx, id)
		if err != nil {
			return nil //nolint:nilerr // status already persisted; stamping is best-effort
		}
		s.LastConnectedAt = &now
		s.UpdatedAt = now
		_ = a.repo.Update(ctx, s)
	}
	return nil
}

// ---------------------------------------------------------------------------
// wa.EventSink: stream.Publisher.Publish returns an error; the manager's sink
// is fire-and-forget (no return). This adapter logs publish failures.
// ---------------------------------------------------------------------------

// publisher is the slice of *stream.Publisher this adapter needs.
type publisher interface {
	Publish(ctx context.Context, e domain.Event) error
}

// EventSinkAdapter adapts a *stream.Publisher (Publish returning error) to the
// fire-and-forget wa.EventSink the manager expects.
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

var _ wa.EventSink = (*EventSinkAdapter)(nil)

func (a *EventSinkAdapter) Publish(ctx context.Context, evt domain.Event) {
	if err := a.pub.Publish(ctx, evt); err != nil {
		a.log.WarnContext(ctx, "event publish failed", "event_id", evt.ID, "type", evt.Type, "err", err)
	}
}

// ---------------------------------------------------------------------------
// wa.InboundHandler: wire the real inbound pipeline into the session manager.
// ---------------------------------------------------------------------------

type InboundPipelineHandler struct {
	pipeline *inbound.Pipeline
	log      *slog.Logger
}

func NewInboundPipelineHandler(pipeline *inbound.Pipeline, log *slog.Logger) *InboundPipelineHandler {
	if log == nil {
		log = slog.Default()
	}
	return &InboundPipelineHandler{pipeline: pipeline, log: log}
}

var _ wa.InboundHandler = (*InboundPipelineHandler)(nil)

func (h *InboundPipelineHandler) Handle(ctx context.Context, sessionID, organizationID string, isAdmin bool, evt any) {
	if h.pipeline == nil {
		return
	}
	if err := h.pipeline.Process(ctx, sessionID, organizationID, isAdmin, evt); err != nil {
		h.log.WarnContext(ctx, "inbound pipeline failed",
			"session", sessionID, "organization", organizationID, "type", typeName(evt), "err", err)
	}
}

type InboundNormalizer struct{}

func NewInboundNormalizer() *InboundNormalizer { return &InboundNormalizer{} }

var _ inbound.Normalizer = (*InboundNormalizer)(nil)

func (InboundNormalizer) Normalize(evt any, sessionID, organizationID string) (domain.Event, *inbound.NormalizedMessage, bool) {
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
	return ev, nm, true
}

type InboundRepos struct {
	store *store.Store
}

func NewInboundRepos(st *store.Store) *InboundRepos { return &InboundRepos{store: st} }

var _ inbound.Repos = (*InboundRepos)(nil)

func (r *InboundRepos) UpsertIdentity(ctx context.Context, in inbound.IdentityUpsert) error {
	return r.store.Identities.Upsert(ctx, domain.Identity{
		LID:          in.LID,
		PhoneNumber:  stringPtr(in.PhoneNumber),
		PhoneJID:     stringPtr(in.PhoneJID),
		Name:         stringPtr(in.Name),
		BusinessName: stringPtr(in.BusinessName),
		FirstSeenAt:  in.NowMs,
		UpdatedAt:    in.NowMs,
	})
}

func (r *InboundRepos) UpsertContact(ctx context.Context, in inbound.ContactUpsert) error {
	var dmSeen *int64
	if in.SeenInDM {
		dmSeen = &in.NowMs
	}
	if err := r.store.Contacts.Upsert(ctx, domain.Contact{
		SessionID:     in.SessionID,
		LID:           in.LID,
		Phone:         domain.PhoneFromJID(in.LID),
		SeenInDM:      in.SeenInDM,
		DMFirstSeenAt: dmSeen,
		DMLastSeenAt:  dmSeen,
		MessageCount:  0,
		FirstSeenAt:   in.NowMs,
		LastSeenAt:    in.NowMs,
	}); err != nil {
		return err
	}
	if in.BumpMessageCount {
		return r.store.Contacts.BumpSeen(ctx, in.SessionID, in.LID, in.NowMs)
	}
	return nil
}

func (r *InboundRepos) UpsertGroup(ctx context.Context, in inbound.GroupUpsert) error {
	return r.store.Groups.Upsert(ctx, domain.Group{
		GroupJID:         in.GroupJID,
		Subject:          stringPtr(in.Subject),
		Description:      stringPtr(in.Description),
		OwnerJID:         stringPtr(in.OwnerJID),
		ParticipantCount: in.ParticipantCount,
		IsAnnounce:       in.IsAnnounce,
		IsLocked:         in.IsLocked,
		CreatedAtWA:      in.CreatedAtWA,
		FirstSeenAt:      in.NowMs,
		UpdatedAt:        in.NowMs,
	})
}

func (r *InboundRepos) UpsertGroupMember(ctx context.Context, in inbound.GroupMemberUpsert) error {
	return r.store.GroupMembers.Upsert(ctx, domain.GroupMember{
		SessionID:     in.SessionID,
		GroupJID:      in.GroupJID,
		LID:           in.LID,
		GroupNickname: stringPtr(in.Nickname),
		Role:          in.Role,
		FirstSeenAt:   in.NowMs,
		LastSeenAt:    in.NowMs,
	})
}

func (r *InboundRepos) UpsertChat(ctx context.Context, in inbound.ChatUpsert) error {
	return r.store.Chats.Upsert(ctx, domain.Chat{
		SessionID:     in.SessionID,
		ChatJID:       in.ChatJID,
		Type:          in.Type,
		Name:          stringPtr(in.Name),
		LastMessageAt: int64Ptr(in.LastMessageAt),
	})
}

func (r *InboundRepos) InsertMessage(ctx context.Context, in inbound.MessageInsert) error {
	return r.store.Messages.Upsert(ctx, domain.Message{
		SessionID:       in.SessionID,
		WAMessageID:     in.WAMessageID,
		ChatJID:         in.ChatJID,
		SenderLID:       stringPtr(in.SenderLID),
		SenderJID:       stringPtr(in.SenderJID),
		FromMe:          in.FromMe,
		Direction:       in.Direction,
		Type:            in.Type,
		Body:            stringPtr(in.Body),
		QuotedMessageID: stringPtr(in.QuotedMessageID),
		Mentions:        json.RawMessage(mustMarshalJSON(in.Mentions)),
		HasMedia:        in.HasMedia,
		MediaMeta:       in.MediaMeta,
		Timestamp:       in.TimestampMs,
		RawJSON:         json.RawMessage(in.RawJSON),
		CreatedAt:       in.NowMs,
	})
}

func (r *InboundRepos) MarkMessageEdited(ctx context.Context, sessionID, waMessageID, newBody string) error {
	return r.store.Messages.MarkEdited(ctx, sessionID, waMessageID, newBody)
}

func (r *InboundRepos) MarkMessageDeleted(ctx context.Context, sessionID, waMessageID string) error {
	return r.store.Messages.MarkDeleted(ctx, sessionID, waMessageID)
}

func (r *InboundRepos) UpdateMessageStatus(ctx context.Context, in inbound.MessageStatusUpdate) error {
	for _, id := range in.WAMessageIDs {
		if err := r.store.Messages.UpdateStatus(ctx, in.SessionID, id, in.Status, in.AckLevel, nil); err != nil {
			return err
		}
	}
	return nil
}

func (r *InboundRepos) InsertPollVote(ctx context.Context, in inbound.PollVoteInsert) error {
	_, err := r.store.PollVotes.Insert(ctx, domain.PollVote{
		SessionID:       in.SessionID,
		PollMessageID:   in.PollMessageID,
		VoterLID:        in.VoterLID,
		SelectedOptions: json.RawMessage(in.SelectedOptions),
		Timestamp:       in.TimestampMs,
		RawJSON:         json.RawMessage(in.RawJSON),
	})
	return err
}

func (r *InboundRepos) AppendEventLog(ctx context.Context, evt domain.Event) error {
	payload, err := json.Marshal(evt.Payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	_, err = r.store.EventLog.Append(ctx, domain.EventLogEntry{
		EventID:        evt.ID,
		OrganizationID: evt.Organization,
		SessionID:      evt.Session,
		Type:           evt.Type,
		Payload:        payload,
		CreatedAt:      evt.Timestamp,
	})
	return err
}

type inboundWebhookEnqueuer interface {
	Enqueue(ctx context.Context, evt domain.Event) (int, error)
}

type InboundWebhookEnqueuerAdapter struct {
	enqueuer inboundWebhookEnqueuer
}

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
			nm.PollVote = &inbound.NormalizedPollVote{
				PollMessageID:   pr.Message.PollVoteTargetID,
				VoterLID:        pr.Message.SenderLID,
				SelectedOptions: json.RawMessage(mustMarshalJSON(pr.Message.SelectedHashes)),
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
	if nm.IsGroup {
		nm.Group = &inbound.NormalizedGroup{GroupJID: m.ChatJID}
		if senderLID != "" {
			nm.Members = append(nm.Members, inbound.NormalizedMember{
				LID:      senderLID,
				JID:      senderJID,
				Nickname: m.PushName,
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
