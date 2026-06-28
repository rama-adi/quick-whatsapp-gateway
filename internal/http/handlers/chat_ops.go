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
	Session string `path:"session"`
	Limit   int    `query:"limit"`
	Cursor  string `query:"cursor"`
}

// getChatInput is GET /sessions/{session}/chats/{cid}.
type getChatInput struct {
	Session string `path:"session"`
	CID     string `path:"cid"`
}

// listChatMessagesInput is GET /sessions/{session}/chats/{cid}/messages.
type listChatMessagesInput struct {
	Session string `path:"session"`
	CID     string `path:"cid"`
	Limit   int    `query:"limit"`
	Cursor  string `query:"cursor"`
}

// readChatInput is POST /sessions/{session}/chats/{cid}/read.
type readChatInput struct {
	Session string `path:"session"`
	CID     string `path:"cid"`
}

// updateChatInput is PATCH /sessions/{session}/chats/{cid}. Body fields are
// optional on the wire; the service applies/validates them.
type updateChatInput struct {
	Session string `path:"session"`
	CID     string `path:"cid"`
	Body    struct {
		Archived   *bool  `json:"archived,omitempty"`
		Pinned     *bool  `json:"pinned,omitempty"`
		MutedUntil *int64 `json:"mutedUntil,omitempty"`
		Unmute     bool   `json:"unmute,omitempty"`
	}
}

// deleteChatInput is DELETE /sessions/{session}/chats/{cid}.
type deleteChatInput struct {
	Session string `path:"session"`
	CID     string `path:"cid"`
}

// chatPresenceInput is PUT /sessions/{session}/chats/{cid}/presence.
type chatPresenceInput struct {
	Session string `path:"session"`
	CID     string `path:"cid"`
	Body    struct {
		State string `json:"state,omitempty"`
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
