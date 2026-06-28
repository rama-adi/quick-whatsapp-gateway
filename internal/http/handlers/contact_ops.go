package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// listContactsInput is GET /sessions/{session}/contacts (the "found users"
// feature). Filters: ?source=dm|group, ?group={jid}, ?q=.
type listContactsInput struct {
	Session string `path:"session" doc:"The WhatsApp session id (a session is one attached WhatsApp number) whose contacts are listed." example:"sess_01HZX"`
	Source  string `query:"source" doc:"Optional source filter. **dm** keeps only people you have a direct chat with; **group** keeps only people seen in a group. Omit to return contacts from both sources." enum:"dm,group" example:"dm"`
	Group   string `query:"group" doc:"Optional group filter. Pass a group JID (e.g. \"12345-67890@g.us\") to keep only members of that one group. Has no effect unless the contacts were seen in that group." example:"12345-67890@g.us"`
	Q       string `query:"q" doc:"Optional free-text search over each contact's name or phone number. Case-insensitive substring match." example:"alice"`
	Limit   int    `query:"limit" doc:"Maximum number of contacts to return on one page. Clamped server-side to the range 1–200; values outside the range are coerced to the nearest bound. Defaults to 50 when omitted or 0." example:"50"`
	Cursor  string `query:"cursor" doc:"Opaque pagination cursor. Pass the \"nextCursor\" value from the previous response to fetch the next page; omit on the first request. Treat the value as a token — do not parse, construct, or modify it. An empty \"nextCursor\" in the response means the last page was reached." example:"eyJpZCI6MTAwfQ"`
}

// checkContactInput is GET /sessions/{session}/contacts/check?phone=.
type checkContactInput struct {
	Session string `path:"session" doc:"The WhatsApp session id used to perform the live on-WhatsApp lookup. The session must be connected." example:"sess_01HZX"`
	Phone   string `query:"phone" doc:"The phone number to look up, in E.164 form (digits, optionally with a leading +). WhatsApp is queried live to determine whether this number has an account." example:"+14155550123"`
}

// getContactInput is GET /sessions/{session}/contacts/{lid}.
type getContactInput struct {
	Session string `path:"session" doc:"The WhatsApp session id that owns the stored contact." example:"sess_01HZX"`
	LID     string `path:"lid" doc:"The contact's LID — WhatsApp's stable per-account identifier for a person. Served from stored data, so the value must already be known to this session (e.g. from a prior list response)." example:"123456789@lid"`
}

// contactJIDInput is GET /sessions/{session}/contacts/{jid}/(picture|about) and
// POST /sessions/{session}/contacts/{jid}/(block|unblock).
type contactJIDInput struct {
	Session string `path:"session" doc:"The WhatsApp session id used to perform the live action. The session must be connected." example:"sess_01HZX"`
	JID     string `path:"jid" doc:"A WhatsApp JID — the address of a user (e.g. \"14155550123@s.whatsapp.net\"), group (\"...@g.us\"), or channel. For contact picture/about/block/unblock this is the target user's JID." example:"14155550123@s.whatsapp.net"`
}

type contactListOutput struct{ Body apitypes.List[domain.Contact] }
type contactDetailOutput struct{ Body service.ContactDetail }
type contactCheckOutput struct{ Body domain.OnWhatsApp }
type contactPictureOutput struct{ Body domain.ProfilePicture }
type contactAboutOutput struct {
	Body struct {
		About string `json:"about"`
	}
}

// RegisterContactOps registers the contacts ("found users" + live contact)
// operations on the huma API: GETs gated read, block/unblock gated send.
// Code-first replacement for the chi contacts groups.
func RegisterContactOps(api huma.API, h *Handlers) {
	read := huma.Middlewares{humax.RequireCap(api, authz.CapRead)}
	send := huma.Middlewares{humax.RequireCap(api, authz.CapSend)}

	huma.Register(api, huma.Operation{
		OperationID: "listContacts", Method: "GET", Path: "/api/v1/sessions/{session}/contacts",
		Summary: "List a session's contacts (found users)",
		Description: "Returns the people this session has seen, one page at a time.\n\n" +
			"Served entirely from the gateway's **stored** WhatsApp data, so it works even when the " +
			"session is currently disconnected — no live WhatsApp connection is required.\n\n" +
			"**Filters** (all optional, combinable):\n" +
			"- `source=dm` — keep only people you have a direct chat with.\n" +
			"- `source=group` — keep only people seen in a group.\n" +
			"- `group={jid}` — keep only members of the given group JID.\n" +
			"- `q=` — case-insensitive substring search over each contact's name or number.\n\n" +
			"**Pagination:** results are cursor-paged. Use `limit` (1–200, default 50) to size the page " +
			"and pass the response's `nextCursor` back as `cursor` to fetch the next page. An empty " +
			"`nextCursor` means there are no more pages.\n\n" +
			"**Auth:** requires the `read` capability.\n\n" +
			"**Errors:** `404` (`not_found`) if the session does not exist or is not owned by the caller's " +
			"organization; `400` (`validation_error`) if a query parameter is malformed.",
		Tags: []string{"Contacts"}, Middlewares: read,
	}, func(ctx context.Context, in *listContactsInput) (*contactListOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		f := store.ContactFilter{Source: in.Source, GroupJID: in.Group, Q: in.Q}
		page, err := h.Contacts.List(ctx, org, in.Session, f, in.Cursor, clampLimit(in.Limit))
		if err != nil {
			return nil, humax.Err(err)
		}
		return &contactListOutput{Body: apitypes.NewList(page.Items, page.NextCursor)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "checkContact", Method: "GET", Path: "/api/v1/sessions/{session}/contacts/check",
		Summary: "Check whether a phone number is on WhatsApp",
		Description: "Asks WhatsApp whether the given `phone` number has an account and, if so, returns its " +
			"JID (WhatsApp's internal address).\n\n" +
			"This is a **live lookup** against WhatsApp, not a read of stored data — the session must be " +
			"**connected**. If the session is not connected the gateway responds `501` (`not_implemented`).\n\n" +
			"The lookup is read-only and has no side effects; it neither adds the number as a contact nor " +
			"notifies the other party.\n\n" +
			"**Auth:** requires the `read` capability.\n\n" +
			"**Errors:** `400` (`validation_error`) if `phone` is missing or malformed; `404` (`not_found`) " +
			"if the session does not exist or is not owned by the caller's organization; `501` " +
			"(`not_implemented`) if the session is not connected so the live lookup cannot run.",
		Tags: []string{"Contacts"}, Middlewares: read,
	}, func(ctx context.Context, in *checkContactInput) (*contactCheckOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		res, err := h.Contacts.Check(ctx, org, in.Session, in.Phone)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &contactCheckOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getContact", Method: "GET", Path: "/api/v1/sessions/{session}/contacts/{lid}",
		Summary: "Get one contact",
		Description: "Returns everything stored for one contact, addressed by its `lid` (WhatsApp's stable " +
			"per-account identifier for a person): their name, whether you have a direct chat with them, " +
			"and every group you have seen them in — including their nickname and role in each group.\n\n" +
			"Served entirely from **stored** data, so no live WhatsApp connection is needed.\n\n" +
			"**Auth:** requires the `read` capability.\n\n" +
			"**Errors:** `404` (`not_found`) if the session does not exist, is not owned by the caller's " +
			"organization, or no contact with the given `lid` is stored for that session.",
		Tags: []string{"Contacts"}, Middlewares: read,
	}, func(ctx context.Context, in *getContactInput) (*contactDetailOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		detail, err := h.Contacts.Get(ctx, org, in.Session, in.LID)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &contactDetailOutput{Body: detail}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getContactPicture", Method: "GET", Path: "/api/v1/sessions/{session}/contacts/{jid}/picture",
		Summary: "Get a contact's profile picture",
		Description: "Fetches the contact's current profile picture from WhatsApp and returns its URL and " +
			"metadata.\n\n" +
			"This is a **live lookup** against WhatsApp — the session must be **connected**. If the session " +
			"is not connected the gateway responds `501` (`not_implemented`). The returned URL points at " +
			"WhatsApp's CDN and is time-limited; fetch it promptly.\n\n" +
			"**Auth:** requires the `read` capability.\n\n" +
			"**Errors:** `404` (`not_found`) if the session does not exist or is not owned by the caller's " +
			"organization (also returned when the contact has no accessible picture, depending on privacy " +
			"settings); `501` (`not_implemented`) if the session is not connected.",
		Tags: []string{"Contacts"}, Middlewares: read,
	}, func(ctx context.Context, in *contactJIDInput) (*contactPictureOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		pic, err := h.Contacts.Picture(ctx, org, in.Session, in.JID)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &contactPictureOutput{Body: pic}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getContactAbout", Method: "GET", Path: "/api/v1/sessions/{session}/contacts/{jid}/about",
		Summary: "Get a contact's about text",
		Description: "Fetches the contact's \"about\" text — the short status line shown on their profile — " +
			"from WhatsApp.\n\n" +
			"This is a **live lookup** against WhatsApp — the session must be **connected**. If the session " +
			"is not connected the gateway responds `501` (`not_implemented`). The `about` field may be an " +
			"empty string when the contact has none or has restricted it by privacy settings.\n\n" +
			"**Auth:** requires the `read` capability.\n\n" +
			"**Errors:** `404` (`not_found`) if the session does not exist or is not owned by the caller's " +
			"organization; `501` (`not_implemented`) if the session is not connected.",
		Tags: []string{"Contacts"}, Middlewares: read,
	}, func(ctx context.Context, in *contactJIDInput) (*contactAboutOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		about, err := h.Contacts.About(ctx, org, in.Session, in.JID)
		if err != nil {
			return nil, humax.Err(err)
		}
		out := &contactAboutOutput{}
		out.Body.About = about
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "blockContact", Method: "POST", Path: "/api/v1/sessions/{session}/contacts/{jid}/block",
		Summary: "Block a contact",
		Description: "Tells WhatsApp to block this contact so they can no longer message the session.\n\n" +
			"This is a **live action** against WhatsApp — the session must be **connected**. If the session " +
			"is not connected the gateway responds `501` (`not_implemented`).\n\n" +
			"The operation is **idempotent**: blocking an already-blocked contact succeeds with the same " +
			"`204` and no additional effect. On success the response body is empty.\n\n" +
			"**Auth:** requires the `send` capability.\n\n" +
			"**Errors:** `404` (`not_found`) if the session does not exist or is not owned by the caller's " +
			"organization; `501` (`not_implemented`) if the session is not connected.",
		Tags:          []string{"Contacts"},
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *contactJIDInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Contacts.SetBlocked(ctx, org, in.Session, in.JID, true); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "unblockContact", Method: "POST", Path: "/api/v1/sessions/{session}/contacts/{jid}/unblock",
		Summary: "Unblock a contact",
		Description: "Tells WhatsApp to unblock this contact so they can message the session again.\n\n" +
			"This is a **live action** against WhatsApp — the session must be **connected**. If the session " +
			"is not connected the gateway responds `501` (`not_implemented`).\n\n" +
			"The operation is **idempotent**: unblocking a contact who is not blocked succeeds with the same " +
			"`204` and no additional effect. On success the response body is empty.\n\n" +
			"**Auth:** requires the `send` capability.\n\n" +
			"**Errors:** `404` (`not_found`) if the session does not exist or is not owned by the caller's " +
			"organization; `501` (`not_implemented`) if the session is not connected.",
		Tags:          []string{"Contacts"},
		DefaultStatus: 204, Middlewares: send,
	}, func(ctx context.Context, in *contactJIDInput) (*emptyOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.Contacts.SetBlocked(ctx, org, in.Session, in.JID, false); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})
}
