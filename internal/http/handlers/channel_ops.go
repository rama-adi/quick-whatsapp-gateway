package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

// createChannelInput is POST /sessions/{session}/channels. Body fields are
// optional on the wire so the service stays the validator (domain.ErrValidation
// → 400), matching the chi handler's behavior.
type createChannelInput struct {
	Session string `path:"session" doc:"The WhatsApp session id — one attached WhatsApp number — that will own the new channel. The session must exist and be connected." example:"01HX..."`
	Body    struct {
		Name        string `json:"name,omitempty" doc:"Display name of the channel (newsletter). Optional on the wire; the service validates it and returns a validation_error (400) if it is missing or invalid." example:"Acme Announcements"`
		Description string `json:"description,omitempty" doc:"Optional free-text description shown on the channel profile." example:"Product updates and release notes."`
	}
}

// channelJIDInput is the no-payload action shape (:follow / :unfollow).
type channelJIDInput struct {
	Session string `path:"session" doc:"The WhatsApp session id (one attached WhatsApp number) performing the follow/unfollow." example:"01HX..."`
	JID     string `path:"jid" doc:"The channel's WhatsApp JID (a newsletter address, e.g. \"120363012345678901@newsletter\"). Percent-encode reserved characters such as \"@\" (\"%40\") in the path — the gateway URL-decodes it before use." example:"120363012345678901@newsletter"`
}

// muteChannelInput is POST /sessions/{session}/channels/{jid}:mute. The body is
// optional; an absent {"mute":...} defaults to muting (matching the chi handler).
type muteChannelInput struct {
	Session string `path:"session" doc:"The WhatsApp session id (one attached WhatsApp number) whose mute state for this channel is being changed." example:"01HX..."`
	JID     string `path:"jid" doc:"The channel's WhatsApp JID (a newsletter address, e.g. \"120363012345678901@newsletter\"). Percent-encode reserved characters such as \"@\" (\"%40\") in the path." example:"120363012345678901@newsletter"`
	Body    struct {
		Mute *bool `json:"mute,omitempty" doc:"Whether to mute (true) or unmute (false) the channel. Optional: omit the field (or send an empty/absent body) and the channel is muted (defaults to true)." example:"true"`
	}
}

// listChannelMessagesInput is GET /sessions/{session}/channels/{jid}/messages.
type listChannelMessagesInput struct {
	Session string `path:"session" doc:"The WhatsApp session id (one attached WhatsApp number) whose stored channel messages are queried." example:"01HX..."`
	JID     string `path:"jid" doc:"The channel's WhatsApp JID (a newsletter address, e.g. \"120363012345678901@newsletter\"). Percent-encode reserved characters such as \"@\" (\"%40\") in the path." example:"120363012345678901@newsletter"`
	Limit   int    `query:"limit" doc:"Maximum number of messages to return on one page. Optional. Omitted or 0 defaults to 50; values are clamped to the range 1–200." example:"50"`
	Cursor  string `query:"cursor" doc:"Opaque pagination cursor. Omit it for the first page; on each response read \"nextCursor\" and pass it back here to fetch the next page. Treat the value as a token — do not parse or construct it. An empty \"nextCursor\" in the response means there are no more pages." example:""`
}

type createChannelOutput struct {
	Body struct {
		JID string `json:"jid"`
	}
}
type channelMessageListOutput struct{ Body apitypes.List[domain.Message] }

// RegisterChannelOps registers the channel/newsletter operations (send
// capability) on the huma API. Code-first replacement for the channels routes in
// the chi resources group. The 501 not_implemented behavior is preserved: the
// service may return domain.ErrNotImplemented and humax.Err maps it to 501.
func RegisterChannelOps(api huma.API, h *Handlers) {
	send := huma.Middlewares{humax.RequireCap(api, authz.CapSend)}

	huma.Register(api, huma.Operation{
		OperationID: "createChannel", Method: "POST", Path: "/api/v1/sessions/{session}/channels",
		Summary: "Create a channel (newsletter)", Tags: []string{"Channels"},
		Description: "Create a WhatsApp **channel** (newsletter) owned by this session, returning its newly minted JID.\n\n" +
			"**Not implemented in v1.** This endpoint is wired but the underlying WhatsApp operation is not yet built, so it **always responds `501` with `not_implemented`** — no channel is created and nothing is persisted. The request body shape below documents the eventual contract; today it is accepted but ignored.\n\n" +
			"**Authorization:** requires the `send` capability; callers lacking it get `403` (`forbidden`). Requests without a valid principal get `401`.\n\n" +
			"**When implemented**, this will be a write that creates a brand-new channel on the WhatsApp network; it is **not** idempotent (each successful call would create a distinct channel) and on success returns `201` with the channel's `jid`.\n\n" +
			"**Errors:** `403` `forbidden` (missing `send`), `404` `not_found` (session unknown), `501` `not_implemented` (current behavior), and, once built, `400` `validation_error` for a missing/invalid `name`.",
		DefaultStatus: 201, Middlewares: send,
	}, func(ctx context.Context, in *createChannelInput) (*createChannelOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		jid, err := h.Channels.Create(ctx, org, in.Session, in.Body.Name, in.Body.Description)
		if err != nil {
			return nil, humax.Err(err)
		}
		out := &createChannelOutput{}
		out.Body.JID = jid
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "followChannel", Method: "POST", Path: "/api/v1/sessions/{session}/channels/{jid}:follow",
		Summary: "Follow a channel", Tags: []string{"Channels"},
		Description: "Subscribe this session to the channel identified by `jid` so its posts arrive on this account.\n\n" +
			"**Not implemented in v1.** This endpoint is wired but **always responds `501` with `not_implemented`** — no follow is performed and no state changes.\n\n" +
			"**Authorization:** requires the `send` capability (`403` `forbidden` otherwise; `401` without a valid principal).\n\n" +
			"**When implemented**, this will be an **idempotent** write: following an already-followed channel is a no-op and still returns `204` (no content). The action takes no request body.\n\n" +
			"**Errors:** `403` `forbidden` (missing `send`), `404` `not_found` (session or channel unknown), `501` `not_implemented` (current behavior).",
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *channelJIDInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Channels.Follow(ctx, org, in.Session, decodeParam(in.JID)); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "unfollowChannel", Method: "POST", Path: "/api/v1/sessions/{session}/channels/{jid}:unfollow",
		Summary: "Unfollow a channel", Tags: []string{"Channels"},
		Description: "Unsubscribe this session from the channel identified by `jid` so its posts stop arriving on this account.\n\n" +
			"**Not implemented in v1.** This endpoint is wired but **always responds `501` with `not_implemented`** — no unfollow is performed and no state changes.\n\n" +
			"**Authorization:** requires the `send` capability (`403` `forbidden` otherwise; `401` without a valid principal).\n\n" +
			"**When implemented**, this will be an **idempotent** write: unfollowing a channel that is not followed is a no-op and still returns `204` (no content). The action takes no request body.\n\n" +
			"**Errors:** `403` `forbidden` (missing `send`), `404` `not_found` (session or channel unknown), `501` `not_implemented` (current behavior).",
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *channelJIDInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Channels.Unfollow(ctx, org, in.Session, decodeParam(in.JID)); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "muteChannel", Method: "POST", Path: "/api/v1/sessions/{session}/channels/{jid}:mute",
		Summary: "Mute or unmute a channel", Tags: []string{"Channels"},
		Description: "Set this session's notification state for the channel identified by `jid`.\n\n" +
			"The request body is optional. Send `{\"mute\": true}` to mute, `{\"mute\": false}` to unmute. If the field is omitted (or the body is empty/absent), the channel is **muted** (the default is `true`).\n\n" +
			"**Not implemented in v1.** This endpoint is wired but **always responds `501` with `not_implemented`** — the mute state is not changed.\n\n" +
			"**Authorization:** requires the `send` capability (`403` `forbidden` otherwise; `401` without a valid principal).\n\n" +
			"**When implemented**, this will be an **idempotent** write that sets (not toggles) the mute state, returning `204` (no content) on success regardless of the prior state.\n\n" +
			"**Errors:** `403` `forbidden` (missing `send`), `404` `not_found` (session or channel unknown), `501` `not_implemented` (current behavior).",
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *muteChannelInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		mute := true // default to muting; pass {"mute":false} to unmute
		if in.Body.Mute != nil {
			mute = *in.Body.Mute
		}
		if err := h.Channels.Mute(ctx, org, in.Session, decodeParam(in.JID), mute); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "listChannelMessages", Method: "GET", Path: "/api/v1/sessions/{session}/channels/{jid}/messages",
		Summary: "List stored channel messages", Tags: []string{"Channels"}, Middlewares: send,
		Description: "Return a page of **stored** messages for the channel identified by `jid` on this session, newest-first.\n\n" +
			"This reads from the gateway's own message store — it returns what the gateway has already received and persisted for the channel, not a live fetch from WhatsApp. A channel the session has never received posts from yields an empty page.\n\n" +
			"**Pagination:** use `limit` (1–200, default 50) and `cursor`. Omit `cursor` for the first page; read `nextCursor` from the response and pass it back to fetch the next page. The cursor is opaque — do not parse it. An empty `nextCursor` means the last page has been reached.\n\n" +
			"**Authorization:** requires the `send` capability (`403` `forbidden` otherwise; `401` without a valid principal). This is a safe, side-effect-free read.\n\n" +
			"**Errors:** `403` `forbidden` (missing `send`), `404` `not_found` (session or channel unknown).",
	}, func(ctx context.Context, in *listChannelMessagesInput) (*channelMessageListOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		page, err := h.Channels.Messages(ctx, org, in.Session, decodeParam(in.JID), in.Cursor, clampLimit(in.Limit))
		if err != nil {
			return nil, humax.Err(err)
		}
		return &channelMessageListOutput{Body: apitypes.NewList(page.Items, page.NextCursor)}, nil
	})
}
