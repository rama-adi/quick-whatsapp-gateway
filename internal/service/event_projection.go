package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/application"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// This file implements the API-side projections over committed gateway events
// (plan §10/§11): the WhatsApp-data rows the legacy gateway wrote through its
// local inbound pipeline are now derived here, from the durable event envelope,
// after the ingest transaction commits. Consumers must treat Event.ID as their
// idempotency backstop — a committed event can be replayed after an
// acknowledgement is lost, and every write below is an idempotent upsert keyed
// by the natural key the pipeline always used.

// projectionStore is the slice of the store aggregate the projection consumers
// need. *store.Store satisfies it; tests inject fakes.
type projectionStore interface {
	UpsertIdentity(ctx context.Context, in ProjectionIdentityUpsert) error
	FillIdentityName(ctx context.Context, jid, name string, nowMs int64) error
	UpsertGroup(ctx context.Context, in ProjectionGroupUpsert) error
	UpsertGroupMember(ctx context.Context, in ProjectionGroupMemberUpsert) error
	UpsertChat(ctx context.Context, in ProjectionChatUpsert) error
	InsertMessage(ctx context.Context, in ProjectionMessageInsert) error
	MarkMessageEdited(ctx context.Context, sessionID, waMessageID, newBody string) error
	MarkMessageDeleted(ctx context.Context, sessionID, waMessageID string) error
	UpdateMessageStatus(ctx context.Context, in ProjectionMessageStatusUpdate) error
	UpsertPoll(ctx context.Context, in ProjectionPollUpsert) error
	InsertPollVote(ctx context.Context, in ProjectionPollVoteInsert) error
	// Session pairing lifecycle: the wa_sessions row is API-owned, so PairSuccess
	// and logout events are the durable signals that attach or clear identity.
	AttachPairing(ctx context.Context, in store.AttachPairingInput) error
	ClearPairing(ctx context.Context, sessionID string, updatedAt int64) error
}

// The Projection* structs mirror the argument structs the gateway's inbound
// Repos consumed (internal/wa/inbound ports), expressed with plain store types.
// They exist so this package's consumers stay decoupled from any single repo
// method signature set and remain unit-testable without MySQL.

type ProjectionIdentityUpsert struct {
	LID          string
	PhoneNumber  string
	PhoneJID     string
	Name         string
	BusinessName string
	NowMs        int64
}

type ProjectionChatUpsert struct {
	SessionID     string
	ChatJID       string
	Type          domain.ChatType
	Name          string
	LastMessageAt int64
	NowMs         int64
}

type ProjectionMessageInsert struct {
	SessionID       string
	WAMessageID     string
	ChatJID         string
	SenderLID       string
	SenderJID       string
	FromMe          bool
	Direction       domain.MessageDirection
	Type            string
	Body            string
	QuotedMessageID string
	Mentions        []string
	HasMedia        bool
	MediaMeta       *domain.MediaMeta
	TimestampMs     int64
	RawJSON         []byte
	NowMs           int64
}

type ProjectionMessageStatusUpdate struct {
	SessionID    string
	WAMessageIDs []string
	Status       domain.MessageStatus
	AckLevel     *int
	NowMs        int64
}

type ProjectionPollUpsert struct {
	SessionID       string
	PollMessageID   string
	ChatJID         string
	Name            string
	Options         []string
	SelectableCount int
	EndTime         int64
	HideVotes       bool
	NowMs           int64
}

type ProjectionPollVoteInsert struct {
	SessionID       string
	PollMessageID   string
	VoterLID        string
	SelectedOptions []byte
	TimestampMs     int64
	RawJSON         []byte
}

type ProjectionGroupUpsert struct {
	GroupJID         string
	Subject          string
	Description      string
	OwnerJID         string
	ParticipantCount *int
	IsAnnounce       *bool
	IsLocked         *bool
	CreatedAtWA      *int64
	NowMs            int64
}

type ProjectionGroupMemberUpsert struct {
	SessionID string
	GroupJID  string
	LID       string
	Tag       string
	Role      domain.GroupRole
	NowMs     int64
}

// EventProjectionConsumer applies one committed event to the WhatsApp-data
// tables. Events it cannot decode or does not project succeed as no-ops: the
// envelope itself is already durably stored in event_log by ingest.
//
// It replaces the gateway-local inbound capture/persist stages (plan §10/§11):
// chats, messages, polls, poll votes, receipt statuses, identities, and sender
// memberships are now derived API-side from committed events.
type EventProjectionConsumer struct {
	store projectionStore
	clock func() int64
}

// NewEventProjectionConsumer builds the shared projection sink. clock may be
// nil (domain.NowMs is used).
func NewEventProjectionConsumer(store projectionStore, clock func() int64) *EventProjectionConsumer {
	if clock == nil {
		clock = domain.NowMs
	}
	return &EventProjectionConsumer{store: store, clock: clock}
}

var _ application.CommittedEventConsumer = (*EventProjectionConsumer)(nil)

// ConsumeCommittedEvent projects message-family events. All other catalog
// events carry no WhatsApp-data rows (presence/calls/newsletters are ephemeral;
// session/auth state is API-owned elsewhere).
func (c *EventProjectionConsumer) ConsumeCommittedEvent(ctx context.Context, event domain.Event) error {
	if c == nil || c.store == nil {
		return nil
	}
	switch event.Type {
	case domain.EventMessage, domain.EventMessageFromMe:
		return c.projectMessage(ctx, event)
	case domain.EventMessageReaction:
		return c.projectReaction(ctx, event)
	case domain.EventMessageEdited:
		return c.projectEdit(ctx, event)
	case domain.EventMessageRevoked:
		return c.projectRevoke(ctx, event)
	case domain.EventPollVote:
		return c.projectPollVote(ctx, event)
	case domain.EventMessageStatus:
		return c.projectReceipt(ctx, event)
	case domain.EventAuthCode:
		return c.projectPairSuccess(ctx, event)
	case domain.EventSessionStatus:
		return c.projectSessionStatus(ctx, event)
	default:
		// presence.update / group.update / group.participant / chat.update /
		// contact.update / call.incoming / newsletter.update / poll.recap: no
		// messages-table projection. Identity and group captures run below where
		// sender/group info is present on the message-family payloads only.
		return nil
	}
}

// payloadJSON re-encodes the decoded payload so persistence keeps the exact
// normalized JSON the legacy pipeline stored in raw_json columns.
func payloadJSON(event domain.Event) json.RawMessage {
	raw, err := json.Marshal(event.Payload)
	if err != nil {
		return nil
	}
	return raw
}

func decodePayload[T any](event domain.Event, out *T) error {
	switch p := event.Payload.(type) {
	case T:
		*out = p
		return nil
	case json.RawMessage:
		return json.Unmarshal(p, out)
	case []byte:
		return json.Unmarshal(p, out)
	default:
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	}
}

// projectMessage upserts chat-before-message (the FK target exists and the
// chat's last_message_at advances), records poll creation metadata, and runs
// identity + sender-membership capture exactly as the legacy capture stage did.
func (c *EventProjectionConsumer) projectMessage(ctx context.Context, event domain.Event) error {
	var p apitypes.MessagePayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	now := c.clock()
	ct := chatTypeFromJID(p.ChatJID)
	senderLID := p.SenderLID
	senderJID := p.SenderJID

	// Chat upsert first so the message has a home.
	if p.ChatJID != "" {
		chatName := ""
		if ct != domain.ChatGroup {
			chatName = p.PushName
		}
		if err := c.store.UpsertChat(ctx, ProjectionChatUpsert{
			SessionID:     event.Session,
			ChatJID:       p.ChatJID,
			Type:          ct,
			Name:          chatName,
			LastMessageAt: p.Timestamp,
			NowMs:         now,
		}); err != nil {
			return err
		}
	}

	dir := domain.DirectionIn
	if p.FromMe {
		dir = domain.DirectionOut
	}
	body := p.Body
	noDisplayableContent := body == "" && p.Location == nil && p.Contact == nil && p.Poll == nil
	if noDisplayableContent {
		// Keep the legacy behavior of storing the normalized payload JSON as the
		// row body when the content had no displayable text (system/media frames);
		// the type column carries the rest.
		if raw := payloadJSON(event); len(raw) > 0 {
			body = string(raw)
		}
	}
	if err := c.store.InsertMessage(ctx, ProjectionMessageInsert{
		SessionID:       event.Session,
		WAMessageID:     p.WAMessageID,
		ChatJID:         p.ChatJID,
		SenderLID:       senderLID,
		SenderJID:       senderJID,
		FromMe:          p.FromMe,
		Direction:       dir,
		Type:            p.Type,
		Body:            body,
		QuotedMessageID: p.QuotedMessageID,
		Mentions:        mentionKeys(p.Mentions),
		HasMedia:        p.HasMedia,
		MediaMeta:       p.Media,
		TimestampMs:     p.Timestamp,
		RawJSON:         payloadJSON(event),
		NowMs:           now,
	}); err != nil {
		return err
	}

	if p.Poll != nil {
		if err := c.store.UpsertPoll(ctx, ProjectionPollUpsert{
			SessionID:       event.Session,
			PollMessageID:   p.WAMessageID,
			ChatJID:         p.ChatJID,
			Name:            p.Poll.Name,
			Options:         p.Poll.Options,
			SelectableCount: p.Poll.SelectableCount,
			EndTime:         p.Poll.EndTime,
			HideVotes:       p.Poll.HideVotes,
			NowMs:           now,
		}); err != nil {
			return err
		}
	}

	// Identity capture (push name preferred) — receipts never reach here because
	// KindReceipt projects through projectReceipt, matching the legacy rule that
	// only message-bearing events bump contact freshness.
	if senderLID != "" {
		if err := c.store.UpsertIdentity(ctx, ProjectionIdentityUpsert{
			LID:         senderLID,
			PhoneJID:    senderJID,
			PhoneNumber: phoneFromJID(senderJID),
			Name:        p.PushName,
			NowMs:       now,
		}); err != nil {
			return err
		}
	} else if p.PushName != "" && senderJID != "" {
		if err := c.store.FillIdentityName(ctx, senderJID, p.PushName, now); err != nil {
			return err
		}
	}

	// Group chats record the sender's membership (role defaults to member).
	if ct == domain.ChatGroup && senderLID != "" {
		if err := c.store.UpsertGroupMember(ctx, ProjectionGroupMemberUpsert{
			SessionID: event.Session,
			GroupJID:  p.ChatJID,
			LID:       senderLID,
			Role:      domain.RoleMember,
			NowMs:     now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// projectReaction refreshes identity/contact freshness but writes no message
// row — reactions live inside their target message's lifecycle, mirroring the
// legacy PersistMessageReaction -> KindOther path.
func (c *EventProjectionConsumer) projectReaction(ctx context.Context, event domain.Event) error {
	var p apitypes.MessagePayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	return c.captureSenderOnly(ctx, event, p)
}

func (c *EventProjectionConsumer) projectEdit(ctx context.Context, event domain.Event) error {
	var p apitypes.MessagePayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.TargetID == "" {
		return nil
	}
	if err := c.store.MarkMessageEdited(ctx, event.Session, p.TargetID, p.Body); err != nil {
		return err
	}
	return nil
}

func (c *EventProjectionConsumer) projectRevoke(ctx context.Context, event domain.Event) error {
	var p apitypes.MessagePayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.TargetID == "" {
		return nil
	}
	return c.store.MarkMessageDeleted(ctx, event.Session, p.TargetID)
}

func (c *EventProjectionConsumer) projectPollVote(ctx context.Context, event domain.Event) error {
	var p apitypes.MessagePayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	voterKey := p.SenderLID
	if voterKey == "" {
		voterKey = p.SenderJID
	}
	selected, _ := json.Marshal(p.SelectedOptions)
	if err := c.store.InsertPollVote(ctx, ProjectionPollVoteInsert{
		SessionID:       event.Session,
		PollMessageID:   p.TargetID,
		VoterLID:        voterKey,
		SelectedOptions: selected,
		TimestampMs:     p.Timestamp,
		RawJSON:         payloadJSON(event),
	}); err != nil {
		return err
	}
	return c.captureSenderOnly(ctx, event, p)
}

// projectReceipt updates status/ack_level monotonically per acked message id.
// The wire payload carries no ack level, so it stays unset (the store derives
// it from status).
func (c *EventProjectionConsumer) projectReceipt(ctx context.Context, event domain.Event) error {
	var p apitypes.MessageStatusPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if len(p.MessageIDs) == 0 {
		return nil
	}
	return c.store.UpdateMessageStatus(ctx, ProjectionMessageStatusUpdate{
		SessionID:    event.Session,
		WAMessageIDs: p.MessageIDs,
		Status:       domain.MessageStatus(p.Status),
		NowMs:        c.clock(),
	})
}

// projectPairSuccess persists the pairing identity carried by an auth.code
// event (the PairSuccess signal): device JID, LID, and the phone number derived
// from the JID. Desired-state reconciliation derives each assignment's
// DeviceJID from wa_sessions.wa_jid, so this projection is what lets a freshly
// paired session actually start. Replays (lost acknowledgement) rewrite the
// same identity and are harmless; a logout that raced ahead of this replay is
// repaired by the next logout's clear.
func (c *EventProjectionConsumer) projectPairSuccess(ctx context.Context, event domain.Event) error {
	var p apitypes.AuthCodePayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.JID == "" {
		// A phone-number pairing kickoff also emits auth.code with only the code;
		// identity attaches at PairSuccess, which carries the JID.
		return nil
	}
	return c.store.AttachPairing(ctx, store.AttachPairingInput{
		SessionID:   event.Session,
		WaJID:       p.JID,
		WaLID:       p.LID,
		PhoneNumber: phoneFromJID(p.JID),
		UpdatedAt:   c.clock(),
	})
}

// projectSessionStatus applies the durable pairing reset when a session is
// force-logged-out by WhatsApp (terminal LoggedOut event): the row is marked
// logged_out with its WhatsApp identity cleared. Explicit logouts already wrote
// the clear synchronously in SessionService.Logout; repeating it here is
// idempotent and repairs any row the synchronous write missed.
func (c *EventProjectionConsumer) projectSessionStatus(ctx context.Context, event domain.Event) error {
	var p apitypes.SessionStatusPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if domain.SessionStatus(p.Status) != domain.SessionLoggedOut {
		return nil
	}
	return c.store.ClearPairing(ctx, event.Session, c.clock())
}

// captureSenderOnly runs the identity half of capture for message-family
// events that do not insert message rows (reactions, votes).
func (c *EventProjectionConsumer) captureSenderOnly(
	ctx context.Context,
	event domain.Event,
	p apitypes.MessagePayload,
) error {
	now := c.clock()
	if p.SenderLID != "" {
		return c.store.UpsertIdentity(ctx, ProjectionIdentityUpsert{
			LID:         p.SenderLID,
			PhoneJID:    p.SenderJID,
			PhoneNumber: phoneFromJID(p.SenderJID),
			Name:        p.PushName,
			NowMs:       now,
		})
	}
	if p.PushName != "" && p.SenderJID != "" {
		return c.store.FillIdentityName(ctx, p.SenderJID, p.PushName, now)
	}
	return nil
}

// mentionKeys flattens the wire mentions map onto the stored mention JID list.
func mentionKeys(m map[string]apitypes.MentionData) []string {
	out := make([]string, 0, len(m))
	for jid := range m {
		out = append(out, jid)
	}
	return out
}

// chatTypeFromJID classifies a chat JID by server suffix. DMs (phone JIDs and
// @lid addresses) are the default, mirroring events.ClassifyChat's mapping.
// Shared by the outbound send recorder and the committed-event projections.
func chatTypeFromJID(jid string) domain.ChatType {
	switch {
	case strings.HasSuffix(jid, "@g.us"):
		return domain.ChatGroup
	case strings.HasSuffix(jid, "@newsletter"):
		return domain.ChatNewsletter
	case strings.HasSuffix(jid, "@broadcast"):
		if strings.HasPrefix(jid, "status@broadcast") {
			return domain.ChatStatus
		}
		return domain.ChatBroadcast
	default:
		return domain.ChatDM
	}
}
