package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
)

// clampLimit reproduces httpx.ParsePage's limit handling for the code-first ops:
// an absent (0) limit defaults to DefaultLimit, and the value is clamped to
// [MinLimit, MaxLimit] so the wire behavior matches the chi handlers exactly.
func clampLimit(limit int) int {
	if limit == 0 {
		limit = httpx.DefaultLimit
	}
	if limit < httpx.MinLimit {
		limit = httpx.MinLimit
	}
	if limit > httpx.MaxLimit {
		limit = httpx.MaxLimit
	}
	return limit
}

// listChatsInput is GET /sessions/{session}/chats.
type listChatsInput struct {
	Session string `path:"session" doc:"The WhatsApp session id (a session is one attached WhatsApp number). The session must be owned by the caller's organization or the request fails with not_found." example:"01HF..."`
	Limit   int    `query:"limit" doc:"Maximum number of chats to return on one page. Clamped to the range 1–200; a missing or zero value defaults to 50." example:"50"`
	Cursor  string `query:"cursor" doc:"Opaque pagination cursor. Pass the nextCursor value from the previous response to fetch the next page; treat it as a token and do not parse it. Omit it to get the first page." example:""`
}

// getChatInput is GET /sessions/{session}/chats/{cid}.
type getChatInput struct {
	Session string `path:"session" doc:"The WhatsApp session id (a session is one attached WhatsApp number). The session must be owned by the caller's organization or the request fails with not_found." example:"01HF..."`
	CID     string `path:"cid" doc:"The chat's JID (Jabber ID) — WhatsApp's address for a conversation. Format is 123...@s.whatsapp.net for a direct chat or 123...@g.us for a group." example:"6281234567890@s.whatsapp.net"`
}

// listChatMessagesInput is GET /sessions/{session}/chats/{cid}/messages.
type listChatMessagesInput struct {
	Session string `path:"session" doc:"The WhatsApp session id (a session is one attached WhatsApp number). The session must be owned by the caller's organization or the request fails with not_found." example:"01HF..."`
	CID     string `path:"cid" doc:"The chat's JID whose messages to list. Format is 123...@s.whatsapp.net for a direct chat or 123...@g.us for a group." example:"6281234567890@s.whatsapp.net"`
	Limit   int    `query:"limit" doc:"Maximum number of messages to return on one page. Clamped to the range 1–200; a missing or zero value defaults to 50." example:"50"`
	Cursor  string `query:"cursor" doc:"Opaque pagination cursor. Pass the nextCursor value from the previous response to fetch the next (older) page; treat it as a token and do not parse it. Omit it to get the first page." example:""`
}

// readChatInput is POST /sessions/{session}/chats/{cid}/read.
type readChatInput struct {
	Session string `path:"session" doc:"The WhatsApp session id (a session is one attached WhatsApp number). The session must be owned by the caller's organization or the request fails with not_found." example:"01HF..."`
	CID     string `path:"cid" doc:"The chat's JID to mark as read. Format is 123...@s.whatsapp.net for a direct chat or 123...@g.us for a group." example:"6281234567890@s.whatsapp.net"`
}

// updateChatInput is PATCH /sessions/{session}/chats/{cid}. Body fields are
// optional on the wire; the service applies/validates them.
type updateChatInput struct {
	Session string `path:"session" doc:"The WhatsApp session id (a session is one attached WhatsApp number). The session must be owned by the caller's organization or the request fails with not_found." example:"01HF..."`
	CID     string `path:"cid" doc:"The chat's JID to update. Format is 123...@s.whatsapp.net for a direct chat or 123...@g.us for a group." example:"6281234567890@s.whatsapp.net"`
	Body    struct {
		Archived   *bool  `json:"archived,omitempty" doc:"Set the chat's archived state. Omit to leave it unchanged." example:"true"`
		Pinned     *bool  `json:"pinned,omitempty" doc:"Set whether the chat is pinned to the top. Omit to leave it unchanged." example:"false"`
		MutedUntil *int64 `json:"mutedUntil,omitempty" doc:"Mute the chat until this instant, expressed as Unix epoch milliseconds. Omit to leave the mute state unchanged. To unmute, send unmute:true instead of this field." example:"1735689600000"`
		Unmute     bool   `json:"unmute,omitempty" doc:"When true, clears any existing mute (overrides mutedUntil). Defaults to false." example:"false"`
	}
}

// deleteChatInput is DELETE /sessions/{session}/chats/{cid}.
type deleteChatInput struct {
	Session string `path:"session" doc:"The WhatsApp session id (a session is one attached WhatsApp number). The session must be owned by the caller's organization or the request fails with not_found." example:"01HF..."`
	CID     string `path:"cid" doc:"The chat's JID to delete from the gateway's stored copy. Format is 123...@s.whatsapp.net for a direct chat or 123...@g.us for a group." example:"6281234567890@s.whatsapp.net"`
}

// chatPresenceInput is PUT /sessions/{session}/chats/{cid}/presence.
type chatPresenceInput struct {
	Session string `path:"session" doc:"The WhatsApp session id (a session is one attached WhatsApp number). The session must be owned by the caller's organization or the request fails with not_found." example:"01HF..."`
	CID     string `path:"cid" doc:"The chat's JID to send the presence indicator to. Format is 123...@s.whatsapp.net for a direct chat or 123...@g.us for a group." example:"6281234567890@s.whatsapp.net"`
	Body    struct {
		State string `json:"state,omitempty" enum:"composing,paused,recording" doc:"The presence state to broadcast to the chat. One of: composing — show a \"typing…\" indicator; recording — show a \"recording audio…\" indicator; paused — clear an active typing/recording indicator. Required." example:"composing"`
	}
}

type chatOutput struct{ Body domain.Chat }
type chatListOutput struct{ Body apitypes.List[domain.Chat] }
type chatMessageListOutput struct{ Body apitypes.List[domain.Message] }

// RegisterChatOps registers the chat viewer + read-state operations on the huma
// API: GETs gated read, mutations gated send. Code-first replacement for the chi
// chats groups.
func RegisterChatOps(api huma.API, h *Handlers) {
	read := huma.Middlewares{humax.RequireCap(api, authz.CapRead)}
	send := huma.Middlewares{humax.RequireCap(api, authz.CapSend)}

	huma.Register(api, huma.Operation{
		OperationID: "listChats", Method: "GET", Path: "/api/v1/sessions/{session}/chats",
		Summary: "List chats", Tags: []string{"Chats"}, Middlewares: read,
		Description: "Returns a page of the session's chats, served from the gateway's **stored copy** (not a live WhatsApp query), ordered for display.\n\n" +
			"Page through results with `limit` and `cursor`: send the `nextCursor` from the previous response back as `cursor` to fetch the next page. When `nextCursor` is `null` there are no more chats.\n\n" +
			"Requires the `read` capability.\n\n" +
			"**Errors**\n\n" +
			"- `not_found` (404): the session does not exist or is not owned by the caller's organization.\n" +
			"- `forbidden` (403): the caller lacks the `read` capability.",
	}, func(ctx context.Context, in *listChatsInput) (*chatListOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		page, err := h.Chats.List(ctx, org, in.Session, in.Cursor, clampLimit(in.Limit))
		if err != nil {
			return nil, humax.Err(err)
		}
		return &chatListOutput{Body: apitypes.NewList(page.Items, page.NextCursor)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getChat", Method: "GET", Path: "/api/v1/sessions/{session}/chats/{cid}",
		Summary: "Get a chat", Tags: []string{"Chats"}, Middlewares: read,
		Description: "Returns one chat by its id (`cid`, the chat JID), served from the gateway's **stored copy**. Use this to read a single chat's archive/pin/mute flags and unread count.\n\n" +
			"Requires the `read` capability.\n\n" +
			"**Errors**\n\n" +
			"- `not_found` (404): the session does not exist, is not owned by the caller's organization, or no chat with that `cid` is stored for the session.\n" +
			"- `forbidden` (403): the caller lacks the `read` capability.",
	}, func(ctx context.Context, in *getChatInput) (*chatOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		chat, err := h.Chats.Get(ctx, org, in.Session, in.CID)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &chatOutput{Body: chat}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "listChatMessages", Method: "GET", Path: "/api/v1/sessions/{session}/chats/{cid}/messages",
		Summary: "List chat messages", Tags: []string{"Chats"}, Middlewares: read,
		Description: "Returns a page of messages in the chat named by `cid`, served from the gateway's **stored copy** (the message history the gateway has ingested), newest first.\n\n" +
			"Page through results with `limit` and `cursor`: send the `nextCursor` from the previous response back as `cursor` to fetch the next (older) page. When `nextCursor` is `null` there are no more messages.\n\n" +
			"Requires the `read` capability.\n\n" +
			"**Errors**\n\n" +
			"- `not_found` (404): the session does not exist, is not owned by the caller's organization, or no chat with that `cid` is stored.\n" +
			"- `forbidden` (403): the caller lacks the `read` capability.",
	}, func(ctx context.Context, in *listChatMessagesInput) (*chatMessageListOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		page, err := h.Chats.ListMessages(ctx, org, in.Session, in.CID, in.Cursor, clampLimit(in.Limit))
		if err != nil {
			return nil, humax.Err(err)
		}
		return &chatMessageListOutput{Body: apitypes.NewList(page.Items, page.NextCursor)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "readChat", Method: "POST", Path: "/api/v1/sessions/{session}/chats/{cid}/read",
		Summary: "Mark a chat as read", Tags: []string{"Chats"}, Middlewares: send,
		Description: "Clears the unread counter on the chat in the path (`cid`) and returns the updated chat.\n\n" +
			"This updates the **gateway's own unread state** — the value the chat viewer reads — and does **not** send per-message read receipts to WhatsApp.\n\n" +
			"**Idempotent:** calling it again on an already-read chat is a no-op and returns the same chat.\n\n" +
			"Requires the `send` capability.\n\n" +
			"**Errors**\n\n" +
			"- `not_found` (404): the session does not exist, is not owned by the caller's organization, or no chat with that `cid` is stored.\n" +
			"- `forbidden` (403): the caller lacks the `send` capability.",
	}, func(ctx context.Context, in *readChatInput) (*chatOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		chat, err := h.Chats.Read(ctx, org, in.Session, in.CID)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &chatOutput{Body: chat}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "updateChat", Method: "PATCH", Path: "/api/v1/sessions/{session}/chats/{cid}",
		Summary: "Update a chat (archive/pin/mute)", Tags: []string{"Chats"}, Middlewares: send,
		Description: "Changes the archive, pin, and mute state of the chat in the path (`cid`) and returns the updated chat.\n\n" +
			"**Partial update:** only the fields you send are changed; omitted fields stay as they are.\n\n" +
			"- `archived` — set the chat's archived state.\n" +
			"- `pinned` — set whether the chat is pinned to the top.\n" +
			"- `mutedUntil` — mute until this instant, as Unix epoch **milliseconds**.\n" +
			"- `unmute` — when `true`, clears any existing mute. Takes precedence over `mutedUntil`.\n\n" +
			"Requires the `send` capability.\n\n" +
			"**Errors**\n\n" +
			"- `not_found` (404): the session does not exist, is not owned by the caller's organization, or no chat with that `cid` is stored.\n" +
			"- `validation_error` (422): the request body is malformed.\n" +
			"- `forbidden` (403): the caller lacks the `send` capability.",
	}, func(ctx context.Context, in *updateChatInput) (*chatOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		chat, err := h.Chats.Update(ctx, org, in.Session, in.CID, service.ChatUpdate{
			Archived:   in.Body.Archived,
			Pinned:     in.Body.Pinned,
			MutedUntil: in.Body.MutedUntil,
			Unmute:     in.Body.Unmute,
		})
		if err != nil {
			return nil, humax.Err(err)
		}
		return &chatOutput{Body: chat}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteChat", Method: "DELETE", Path: "/api/v1/sessions/{session}/chats/{cid}",
		Summary: "Delete a chat", Tags: []string{"Chats"},
		DefaultStatus: 204, Middlewares: send,
		Description: "Removes the chat in the path (`cid`) from the gateway's **stored copy**, along with its locally stored messages.\n\n" +
			"This is a **local delete only**: it does not delete the chat on WhatsApp, and the chat may reappear if new activity is later ingested for it.\n\n" +
			"On success returns **204 No Content** with no body.\n\n" +
			"Requires the `send` capability.\n\n" +
			"**Errors**\n\n" +
			"- `not_found` (404): the session does not exist, is not owned by the caller's organization, or no chat with that `cid` is stored.\n" +
			"- `forbidden` (403): the caller lacks the `send` capability.",
	}, func(ctx context.Context, in *deleteChatInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Chats.Delete(ctx, org, in.Session, in.CID); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "setChatPresence", Method: "PUT", Path: "/api/v1/sessions/{session}/chats/{cid}/presence",
		Summary: "Set chat presence (typing/recording)", Tags: []string{"Chats"},
		DefaultStatus: 204, Middlewares: send,
		Description: "Broadcasts a typing/recording presence indicator to the chat in the path (`cid`). Set `state` to `composing` (\"typing…\"), `recording` (\"recording audio…\"), or `paused` (clear the indicator).\n\n" +
			"**Live client required:** this goes straight to WhatsApp and needs a live, connected client for the session. If the session has no connected client, the call returns **501 `not_implemented`**.\n\n" +
			"On success returns **204 No Content** with no body. The indicator is transient — WhatsApp clears it after a short timeout, so re-send periodically to keep it showing.\n\n" +
			"Requires the `send` capability.\n\n" +
			"**Errors**\n\n" +
			"- `validation_error` (400/422): `state` is missing or not one of the allowed values.\n" +
			"- `not_found` (404): the session does not exist or is not owned by the caller's organization.\n" +
			"- `not_implemented` (501): the session has no connected WhatsApp client to deliver the presence.\n" +
			"- `forbidden` (403): the caller lacks the `send` capability.",
	}, func(ctx context.Context, in *chatPresenceInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Chats.SetPresence(ctx, org, in.Session, in.CID, in.Body.State); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})
}
