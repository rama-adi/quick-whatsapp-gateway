package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// ChatService backs the chat viewer + read-state endpoints (§11 Chats). Read
// paths are served from the store; the per-chat typing presence (PUT
// /chats/{cid}/presence) is delegated to the live PresenceController.
type ChatService struct {
	store    *store.Store
	presence PresenceController
	log      *slog.Logger
}

// NewChatService constructs a ChatService. presence may be nil (the presence
// sub-resource then reports the live client as unavailable).
func NewChatService(s *store.Store, presence PresenceController, log *slog.Logger) *ChatService {
	if log == nil {
		log = slog.Default()
	}
	return &ChatService{store: s, presence: presence, log: log}
}

// requireSession verifies the session exists and belongs to the organization.
func (s *ChatService) requireSession(ctx context.Context, organizationID, sessionID string) error {
	sess, err := s.store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.OrganizationID != organizationID {
		return domain.ErrNotFound("session not found")
	}
	return nil
}

// List returns a page of the session's chats.
func (s *ChatService) List(ctx context.Context, organizationID, sessionID, cursor string, limit int) (store.Page[domain.Chat], error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return store.Page[domain.Chat]{}, err
	}
	return s.store.Chats.ListBySession(ctx, sessionID, cursor, limit)
}

// Get returns a single chat.
func (s *ChatService) Get(ctx context.Context, organizationID, sessionID, chatJID string) (domain.Chat, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return domain.Chat{}, err
	}
	return s.store.Chats.Get(ctx, sessionID, chatJID)
}

// ListMessages returns a page of a chat's messages, with @-mentions resolved to
// display names (MentionNames) so a client can render "@<name>".
func (s *ChatService) ListMessages(ctx context.Context, organizationID, sessionID, chatJID, cursor string, limit int) (store.Page[domain.Message], error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return store.Page[domain.Message]{}, err
	}
	page, err := s.store.Messages.ListByChat(ctx, sessionID, chatJID, cursor, limit)
	if err != nil {
		return store.Page[domain.Message]{}, err
	}
	resolveMentionNames(ctx, s.store.Identities, s.log, page.Items)
	return page, nil
}

// resolveMentionNames fills each message's MentionNames from whatsapp_identities so
// a client can render "@<name>" instead of the raw "@<number>" the body carries. It
// gathers the mention JIDs across the whole page and resolves them in one query; a
// resolution error is logged and left non-fatal (mentions just stay unresolved).
func resolveMentionNames(ctx context.Context, ids *store.IdentityRepo, log *slog.Logger, msgs []domain.Message) {
	if len(msgs) == 0 {
		return
	}
	perMsg := make([][]string, len(msgs))
	var all []string
	for i := range msgs {
		jids := parseMentionJIDs(msgs[i].Mentions)
		if len(jids) == 0 {
			continue
		}
		perMsg[i] = jids
		all = append(all, jids...)
	}
	if len(all) == 0 {
		return
	}
	names, err := ids.NamesForMentions(ctx, all)
	if err != nil {
		log.WarnContext(ctx, "resolve mention names", slog.Any("err", err))
		return
	}
	if len(names) == 0 {
		return
	}
	for i := range msgs {
		if len(perMsg[i]) == 0 {
			continue
		}
		resolved := make(map[string]string, len(perMsg[i]))
		for _, jid := range perMsg[i] {
			up := jidUserPart(jid)
			if n, ok := names[up]; ok {
				resolved[up] = n
			}
		}
		if len(resolved) > 0 {
			msgs[i].MentionNames = resolved
		}
	}
}

// parseMentionJIDs decodes a messages.mentions JSON array (["<jid>", ...]); a nil
// or malformed value yields no mentions.
func parseMentionJIDs(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var jids []string
	if err := json.Unmarshal(raw, &jids); err != nil {
		return nil
	}
	return jids
}

// jidUserPart returns the token before "@" (and any ":device" suffix) — the form
// WhatsApp embeds as "@<userpart>" in a message body.
func jidUserPart(jid string) string {
	if i := strings.IndexAny(jid, "@:"); i >= 0 {
		return jid[:i]
	}
	return jid
}

// Read marks a chat read by zeroing its unread counter (§11 POST /chats/{cid}/read).
// Per-message read receipts to WhatsApp are out of scope for v1; the local
// unread state is the source of truth for the viewer.
func (s *ChatService) Read(ctx context.Context, organizationID, sessionID, chatJID string) (domain.Chat, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return domain.Chat{}, err
	}
	chat, err := s.store.Chats.Get(ctx, sessionID, chatJID)
	if err != nil {
		return domain.Chat{}, err
	}
	if err := s.store.Chats.UpdateFlags(ctx, sessionID, chatJID, chat.Archived, chat.Pinned, chat.MutedUntil, 0); err != nil {
		return domain.Chat{}, err
	}
	chat.UnreadCount = 0
	return chat, nil
}

// ChatUpdate is the PATCH /chats/{cid} mutable surface. Nil fields are unchanged.
type ChatUpdate struct {
	Archived   *bool
	Pinned     *bool
	MutedUntil *int64 // pointer-to-pointer semantics handled by the caller; nil = unchanged
	Unmute     bool   // when true, clears muted_until
}

// Update applies the user-managed chat flags (archive/pin/mute).
func (s *ChatService) Update(ctx context.Context, organizationID, sessionID, chatJID string, in ChatUpdate) (domain.Chat, error) {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return domain.Chat{}, err
	}
	chat, err := s.store.Chats.Get(ctx, sessionID, chatJID)
	if err != nil {
		return domain.Chat{}, err
	}
	if in.Archived != nil {
		chat.Archived = *in.Archived
	}
	if in.Pinned != nil {
		chat.Pinned = *in.Pinned
	}
	if in.Unmute {
		chat.MutedUntil = nil
	} else if in.MutedUntil != nil {
		chat.MutedUntil = in.MutedUntil
	}
	if err := s.store.Chats.UpdateFlags(ctx, sessionID, chatJID, chat.Archived, chat.Pinned, chat.MutedUntil, chat.UnreadCount); err != nil {
		return domain.Chat{}, err
	}
	return chat, nil
}

// Delete removes a chat locally (§11 DELETE /chats/{cid}).
func (s *ChatService) Delete(ctx context.Context, organizationID, sessionID, chatJID string) error {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return err
	}
	return s.store.Chats.Delete(ctx, sessionID, chatJID)
}

// SetPresence sets the per-chat typing state (§11 PUT /chats/{cid}/presence).
func (s *ChatService) SetPresence(ctx context.Context, organizationID, sessionID, chatJID, state string) error {
	if err := s.requireSession(ctx, organizationID, sessionID); err != nil {
		return err
	}
	switch state {
	case "composing", "paused", "recording":
	default:
		return domain.ErrValidation("state must be one of composing, paused, recording")
	}
	if s.presence == nil {
		return errLiveUnavailable()
	}
	return s.presence.SetChatPresence(ctx, sessionID, chatJID, state)
}
