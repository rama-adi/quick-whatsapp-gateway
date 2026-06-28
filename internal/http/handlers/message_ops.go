package handlers

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/wa/outbound"
)

// sendMessageInput is POST /sessions/{session}/messages. The body is the
// discriminated domain.SendRequest; Idempotency-Key (header) and ?async (query)
// tune delivery, mirroring the chi handler. The service validates the body, so it
// is passed through untouched.
type sendMessageInput struct {
	Session        string `path:"session" doc:"The WhatsApp session id (a session is one attached WhatsApp number) that performs the send. Must be a session your organization owns and that is currently connected." example:"01HZX..."`
	Async          bool   `query:"async" doc:"Delivery mode. When **false** (the default) the call blocks until WhatsApp acknowledges the send and the response is **200** with the final SendResult. When **true** the gateway enqueues the send, returns immediately with **202** (status \"queued\"), and the final delivery status arrives later as a \"message.status\" event on the event stream. Async also changes rate-limit behavior: an over-limit synchronous send returns 429, whereas an async send stays queued instead of failing." example:"false"`
	IdempotencyKey string `header:"Idempotency-Key" doc:"Optional client-supplied idempotency token, scoped to your organization. If you retry a send with the same key, the gateway returns the result of the first send and does **not** dispatch a second message to WhatsApp. Use a fresh UUID per logical send and reuse it on retries. Omit it to send unconditionally." example:"2f1c9b6e-7a3d-4c2e-9f8a-1b2c3d4e5f60"`
	Body           domain.SendRequest
}

// sendMessageOutput carries the SendResult. Status is set at runtime to 200
// (sync) or 202 (async), matching the chi handler's status split.
type sendMessageOutput struct {
	Status int
	Body   outbound.SendResult
}

// editMessageInput is PATCH /sessions/{session}/messages/{mid}. Body fields are
// optional on the wire; the service validates.
type editMessageInput struct {
	Session string `path:"session" doc:"The WhatsApp session id that originally sent the message. Must be a connected session your organization owns." example:"01HZX..."`
	MID     string `path:"mid" doc:"The WhatsApp message id (the per-message stable id assigned by WhatsApp) of the message to edit. Must be a text message previously sent by this session." example:"3EB0C431C26A1916E07A"`
	Body    struct {
		Chat string `json:"chat,omitempty" doc:"The JID of the chat the message lives in (e.g. \"123...@s.whatsapp.net\" for a direct chat or \"123...@g.us\" for a group). Identifies which conversation the message id belongs to." example:"6281234567890@s.whatsapp.net"`
		Text string `json:"text,omitempty" doc:"The new text body that replaces the message's current text. Required. Only text messages can be edited — editing media or other kinds is not supported." example:"Updated message text"`
	}
}

// revokeMessageInput is DELETE /sessions/{session}/messages/{mid}. sender is the
// original sender JID ("" for your own message).
type revokeMessageInput struct {
	Session string `path:"session" doc:"The WhatsApp session id used to issue the revoke. Must be a connected session your organization owns." example:"01HZX..."`
	MID     string `path:"mid" doc:"The WhatsApp message id (the per-message stable id assigned by WhatsApp) of the message to delete for everyone." example:"3EB0C431C26A1916E07A"`
	Body    struct {
		Chat   string `json:"chat,omitempty" doc:"The JID of the chat the message lives in (e.g. \"123...@s.whatsapp.net\" for a direct chat or \"123...@g.us\" for a group)." example:"6281234567890@s.whatsapp.net"`
		Sender string `json:"sender,omitempty" doc:"The JID of the original sender of the message. Leave empty (\"\") when revoking your own message; set it to the author's JID when revoking another participant's message (e.g. as a group admin)." example:""`
	}
}

// reactionInput is POST/DELETE /sessions/{session}/messages/{mid}/reaction.
type reactionInput struct {
	Session string `path:"session" doc:"The WhatsApp session id that owns the reaction. Must be a connected session your organization owns." example:"01HZX..."`
	MID     string `path:"mid" doc:"The WhatsApp message id (the per-message stable id assigned by WhatsApp) of the message being reacted to." example:"3EB0C431C26A1916E07A"`
	Body    struct {
		Chat   string `json:"chat,omitempty" doc:"The JID of the chat the message lives in (e.g. \"123...@s.whatsapp.net\" for a direct chat or \"123...@g.us\" for a group)." example:"6281234567890@s.whatsapp.net"`
		Sender string `json:"sender,omitempty" doc:"The JID of the original sender of the message being reacted to. Leave empty (\"\") for your own message; set the author's JID for another participant's message." example:""`
		Emoji  string `json:"emoji,omitempty" doc:"The single emoji to react with. Sending a reaction replaces any reaction you previously set on this message. On the add (POST) endpoint this is required; on the remove (DELETE) endpoint it is ignored — the gateway always sends an empty reaction to clear yours." example:"👍"`
	}
}

// forwardInput is POST /sessions/{session}/messages/{mid}/forward.
type forwardInput struct {
	Session string `path:"session" doc:"The WhatsApp session id used to forward the message. Must be a connected session your organization owns." example:"01HZX..."`
	MID     string `path:"mid" doc:"The WhatsApp message id (the per-message stable id assigned by WhatsApp) of the message to forward." example:"3EB0C431C26A1916E07A"`
	Body    struct {
		Chat   string `json:"chat,omitempty" doc:"The JID of the source chat the message currently lives in (e.g. \"123...@s.whatsapp.net\" or \"123...@g.us\")." example:"6281234567890@s.whatsapp.net"`
		Sender string `json:"sender,omitempty" doc:"The JID of the original sender of the message. Leave empty (\"\") for your own message; set the author's JID for another participant's message." example:""`
		To     string `json:"to,omitempty" doc:"The destination chat JID to forward into (e.g. \"123...@s.whatsapp.net\" for a direct chat or \"123...@g.us\" for a group). Required. Note (v1): the forwarded message is sent as a text reference to the original, marked as forwarded — it does not copy the original body or re-upload media." example:"6289876543210@s.whatsapp.net"`
	}
}

// voteInput is POST /sessions/{session}/messages/{mid}/vote.
type voteInput struct {
	Session string `path:"session" doc:"The WhatsApp session id casting the vote. Must be a connected session your organization owns." example:"01HZX..."`
	MID     string `path:"mid" doc:"The WhatsApp message id (the per-message stable id assigned by WhatsApp) of the poll message being voted on." example:"3EB0C431C26A1916E07A"`
	Body    struct {
		Chat    string   `json:"chat,omitempty" doc:"The JID of the chat the poll lives in (e.g. \"123...@s.whatsapp.net\" or \"123...@g.us\")." example:"120363012345678901@g.us"`
		Sender  string   `json:"sender,omitempty" doc:"The JID of the poll's creator/sender. Identifies the poll message together with mid." example:""`
		Options []string `json:"options,omitempty" doc:"The full set of poll option texts you want selected. This replaces your previous vote entirely — send every option you want marked, not just the new ones. Send an empty array to clear your vote. For single-choice polls supply exactly one option." example:"[\"Pizza\",\"Sushi\"]"`
	}
}

// sendResultOutput is the success body for the message-op endpoints (edit,
// revoke, react, forward, vote) — all 200 with a SendResult body.
type sendResultOutput struct{ Body outbound.SendResult }

// RegisterMessageOps registers the outbound send + message-op operations (send
// capability) on the huma API. Code-first replacement for the chi messages group.
func RegisterMessageOps(api huma.API, h *Handlers) {
	send := huma.Middlewares{humax.RequireCap(api, authz.CapSend)}

	huma.Register(api, huma.Operation{
		OperationID: "sendMessage", Method: "POST", Path: "/api/v1/sessions/{session}/messages",
		Summary: "Send a message",
		Description: "Sends one message from the given session. There is a **single** send endpoint for " +
			"every kind of message; the discriminated `type` field in the request body chooses which one.\n\n" +
			"**Supported in v1:** `text`, `poll`, `location`, and `contact`.\n\n" +
			"**Not implemented yet:** the media types `image`, `video`, `audio`, `document`, and `sticker` " +
			"return **501 `not_implemented`** before any WhatsApp call is made.\n\n" +
			"### Delivery mode (`?async`)\n" +
			"- **Synchronous (default, `?async=false`):** the call blocks until WhatsApp acknowledges the " +
			"send and returns **200** with the final `SendResult`.\n" +
			"- **Asynchronous (`?async=true`):** the gateway persists the send to a queue and returns **202** " +
			"immediately with a queued `SendResult`. The final delivery status arrives later as a " +
			"`message.status` event on the event stream.\n\n" +
			"### Idempotency (`Idempotency-Key` header)\n" +
			"Supply a stable key to make retries safe: replaying a send with a key already seen for your " +
			"organization returns the **original** result and does not dispatch a second WhatsApp message.\n\n" +
			"### Preconditions & errors\n" +
			"- Requires the **`send`** capability.\n" +
			"- The session must exist, be owned by your organization, and be connected.\n" +
			"- **400 `validation_error`** — malformed body or an unsupported/invalid field for the chosen `type`.\n" +
			"- **404 `not_found`** — the session does not exist or is not owned by your organization.\n" +
			"- **429 `rate_limited`** — over the per-session send rate limit. A synchronous send is rejected " +
			"with 429; an async send stays queued instead of failing.\n" +
			"- **501 `not_implemented`** — a media `type` that is not built yet.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *sendMessageInput) (*sendMessageOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		opts := outbound.SendOptions{Async: in.Async, IdempotencyKey: in.IdempotencyKey}
		res, err := h.Messages.Send(ctx, org, in.Session, in.Body, opts)
		if err != nil {
			return nil, humax.Err(err)
		}
		status := http.StatusOK
		if res.Mode == outbound.ModeAsync {
			status = http.StatusAccepted
		}
		return &sendMessageOutput{Status: status, Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "editMessage", Method: "PATCH", Path: "/api/v1/sessions/{session}/messages/{mid}",
		Summary: "Edit a sent text message",
		Description: "Replaces the text body of a message you already sent, identified by `mid` in the path " +
			"and located by `chat` in the body.\n\n" +
			"**Constraints:**\n" +
			"- Only **text** messages can be edited; editing media or other kinds is not supported.\n" +
			"- The message must have been sent by this session.\n\n" +
			"This is a synchronous send to WhatsApp and returns **200** with a `SendResult`.\n\n" +
			"### Preconditions & errors\n" +
			"- Requires the **`send`** capability.\n" +
			"- **400 `validation_error`** — missing/empty `text` or a non-text message.\n" +
			"- **404 `not_found`** — the session or message does not exist or is not owned by your organization.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *editMessageInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.Edit(ctx, org, in.Session, in.Body.Chat, in.MID, in.Body.Text)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revokeMessage", Method: "DELETE", Path: "/api/v1/sessions/{session}/messages/{mid}",
		Summary: "Revoke a message (delete for everyone)",
		Description: "Deletes the message identified by `mid` in the path **for everyone** in the chat — not " +
			"just on your own device. Located by `chat` (and `sender`) in the body.\n\n" +
			"Use the `sender` body field to revoke another participant's message (e.g. as a group admin); " +
			"leave it empty to revoke your own message.\n\n" +
			"This is a synchronous operation and returns **200** with a `SendResult`. WhatsApp enforces its " +
			"own time/role limits on who may revoke what.\n\n" +
			"### Preconditions & errors\n" +
			"- Requires the **`send`** capability.\n" +
			"- **404 `not_found`** — the session or message does not exist or is not owned by your organization.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *revokeMessageInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.Revoke(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "addReaction", Method: "POST", Path: "/api/v1/sessions/{session}/messages/{mid}/reaction",
		Summary: "Add a reaction to a message",
		Description: "Reacts to the message identified by `mid` (located by `chat`/`sender` in the body) with a " +
			"single emoji.\n\n" +
			"A reaction is **idempotent per message**: sending a new reaction replaces any reaction you " +
			"previously set on that message — there is at most one reaction per (you, message) pair.\n\n" +
			"Returns **200** with a `SendResult`.\n\n" +
			"### Preconditions & errors\n" +
			"- Requires the **`send`** capability.\n" +
			"- **400 `validation_error`** — missing/empty `emoji`.\n" +
			"- **404 `not_found`** — the session or message does not exist or is not owned by your organization.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *reactionInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.React(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID, in.Body.Emoji)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "removeReaction", Method: "DELETE", Path: "/api/v1/sessions/{session}/messages/{mid}/reaction",
		Summary: "Remove your reaction from a message",
		Description: "Clears the reaction you previously set on the message identified by `mid` (located by " +
			"`chat`/`sender` in the body). The gateway sends an **empty** reaction to WhatsApp, so any " +
			"`emoji` value in the body is ignored.\n\n" +
			"Idempotent: removing when no reaction is set is a no-op. Returns **200** with a `SendResult`.\n\n" +
			"### Preconditions & errors\n" +
			"- Requires the **`send`** capability.\n" +
			"- **404 `not_found`** — the session or message does not exist or is not owned by your organization.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *reactionInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		// An empty emoji removes the reaction, matching the chi handler.
		res, err := h.Messages.React(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID, "")
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "forwardMessage", Method: "POST", Path: "/api/v1/sessions/{session}/messages/{mid}/forward",
		Summary: "Forward a message to another chat",
		Description: "Forwards the message identified by `mid` (located by `chat`/`sender` in the body) to the " +
			"destination chat named by `to` (a recipient JID).\n\n" +
			"**Note (v1 behavior):** the forwarded message is sent as a **text reference** to the original, " +
			"marked as forwarded — it does **not** copy the original body verbatim or re-upload media. A " +
			"faithful copy is planned once the gateway can fetch a stored message by id.\n\n" +
			"Returns **200** with a `SendResult` describing the newly sent (forwarded) message.\n\n" +
			"### Preconditions & errors\n" +
			"- Requires the **`send`** capability.\n" +
			"- **400 `validation_error`** — missing/empty `to`.\n" +
			"- **404 `not_found`** — the session or source message does not exist or is not owned by your organization.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *forwardInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.Forward(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID, in.Body.To)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sendResultOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "voteMessage", Method: "POST", Path: "/api/v1/sessions/{session}/messages/{mid}/vote",
		Summary: "Vote on a poll message",
		Description: "Casts a vote on the poll message identified by `mid` (located by `chat`/`sender` in the " +
			"body).\n\n" +
			"`options` is the **complete** set of poll choices you want selected — it **replaces** your " +
			"previous vote rather than adding to it. Send an empty array to clear your vote; for a " +
			"single-choice poll send exactly one option.\n\n" +
			"Returns **200** with a `SendResult`.\n\n" +
			"### Preconditions & errors\n" +
			"- Requires the **`send`** capability.\n" +
			"- **400 `validation_error`** — the target is not a poll or an option does not match the poll's choices.\n" +
			"- **404 `not_found`** — the session or poll message does not exist or is not owned by your organization.",
		Tags: []string{"Messages"}, Middlewares: send,
	}, func(ctx context.Context, in *voteInput) (*sendResultOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Messages.Vote(ctx, org, in.Session, in.Body.Chat, in.Body.Sender, in.MID, in.Body.Options)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &sendResultOutput{Body: res}, nil
	})
}
