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

// clampLimit reproduces the same limit defaults and bounds as the chi handler.
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
	Session string `path:"session" doc:"WhatsApp session id. Must belong to the caller organization." example:"01HF..."`
	Limit   int    `query:"limit" doc:"Page size. Missing or 0 uses 50, clamped to 1-200." example:"50"`
	Cursor  string `query:"cursor" doc:"Opaque pagination cursor from the previous response's nextCursor." example:""`
}

// getChatInput is GET /sessions/{session}/chats/{cid}.
type getChatInput struct {
	Session string `path:"session" doc:"WhatsApp session id. Must belong to the caller organization." example:"01HF..."`
	CID     string `path:"cid" doc:"Chat JID (for example 628...@s.whatsapp.net or 120...@g.us)." example:"6281234567890@s.whatsapp.net"`
}

// listChatMessagesInput is GET /sessions/{session}/chats/{cid}/messages.
type listChatMessagesInput struct {
	Session string `path:"session" doc:"WhatsApp session id. Must belong to the caller organization." example:"01HF..."`
	CID     string `path:"cid" doc:"Chat JID to read messages from." example:"6281234567890@s.whatsapp.net"`
	Limit   int    `query:"limit" doc:"Page size. Missing or 0 uses 50, clamped to 1-200." example:"50"`
	Cursor  string `query:"cursor" doc:"Opaque pagination cursor from the previous response's nextCursor." example:""`
}

// readChatInput is POST /sessions/{session}/chats/{cid}/read.
type readChatInput struct {
	Session string `path:"session" doc:"WhatsApp session id. Must belong to the caller organization." example:"01HF..."`
	CID     string `path:"cid" doc:"Chat JID to mark as read." example:"6281234567890@s.whatsapp.net"`
}

// updateChatInput is PATCH /sessions/{session}/chats/{cid}.
type updateChatInput struct {
	Session string `path:"session" doc:"WhatsApp session id. Must belong to the caller organization." example:"01HF..."`
	CID     string `path:"cid" doc:"Chat JID to update." example:"6281234567890@s.whatsapp.net"`
	Body    struct {
		Archived   *bool  `json:"archived,omitempty" doc:"Set archived state." example:"true"`
		Pinned     *bool  `json:"pinned,omitempty" doc:"Set pinned state." example:"false"`
		MutedUntil *int64 `json:"mutedUntil,omitempty" doc:"Mute until Unix epoch milliseconds." example:"1735689600000"`
		Unmute     bool   `json:"unmute,omitempty" doc:"Set true to clear mute." example:"false"`
	}
}

// deleteChatInput is DELETE /sessions/{session}/chats/{cid}.
type deleteChatInput struct {
	Session string `path:"session" doc:"WhatsApp session id. Must belong to the caller organization." example:"01HF..."`
	CID     string `path:"cid" doc:"Chat JID to delete from local storage." example:"6281234567890@s.whatsapp.net"`
}

// chatPresenceInput is PUT /sessions/{session}/chats/{cid}/presence.
type chatPresenceInput struct {
	Session string `path:"session" doc:"WhatsApp session id. Must belong to the caller organization." example:"01HF..."`
	CID     string `path:"cid" doc:"Chat JID receiving presence state." example:"6281234567890@s.whatsapp.net"`
	Body    struct {
		State string `json:"state,omitempty" enum:"composing,paused,recording" doc:"Presence state: composing, recording, or paused." example:"composing"`
	}
}

type chatOutput struct{ Body domain.Chat }
type chatListOutput struct{ Body apitypes.List[domain.Chat] }
type chatMessageListOutput struct{ Body apitypes.List[domain.Message] }

// RegisterChatOps registers chat read/write operations.
func RegisterChatOps(api huma.API, h *Handlers) {
	read := huma.Middlewares{humax.RequireCap(api, authz.CapRead)}
	send := huma.Middlewares{humax.RequireCap(api, authz.CapSend)}

	huma.Register(api, huma.Operation{
		OperationID: "listChats", Method: "GET", Path: "/api/v1/sessions/{session}/chats",
		Summary: "List chats", Tags: []string{"Chats"}, Middlewares: read,
		Description: "Returns chats from stored data for one session.\n\n" +
			"Use `limit` and `cursor` to page results. Requires `read` capability.\n\n" +
			"Errors: `forbidden` if missing `read`, `not_found` if the session is not accessible.",
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
		Description: "Returns one chat from local storage.\n\n" +
			"Requires `read` capability.\n\n" +
			"Errors: `not_found` if the chat is missing, `forbidden` if missing `read`.",
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
		Description: "Returns stored messages for one chat.\n\n" +
			"Use `limit` and `cursor` to page older messages. Requires `read` capability.\n\n" +
			"Errors: `not_found` if chat or session is missing, `forbidden` if missing `read`.",
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
		Description: "Mark chat unread count as zero in local storage.\n\n" +
			"This is a local operation and does not send read receipts.\n\n" +
			"Errors: `not_found` if session or chat is missing, `forbidden` if missing `send`.",
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
		Description: "Apply partial updates to archive, pin, and mute state.\n\n" +
			"Missing fields are left unchanged. Requires `send` capability.\n\n" +
			"Errors: `validation_error` for invalid body, `not_found` if chat missing, `forbidden` if missing `send`.",
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
		Description: "Delete chat and locally stored messages for this session.\n\n" +
			"Local delete only; WhatsApp-side chat remains.\n\n" +
			"Errors: `not_found` if session or chat is missing, `forbidden` if missing `send`.",
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
		Description: "Send a typing/recording indicator (`state`) to a chat.\n\n" +
			"Requires a live connected session.\n\n" +
			"Errors: `validation_error` for bad state, `not_found` for session not found, `not_implemented` if session is not connected, `forbidden` if missing `send`.",
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
