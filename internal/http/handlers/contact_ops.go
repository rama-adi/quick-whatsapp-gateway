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

// listContactsInput is GET /sessions/{session}/contacts.
type listContactsInput struct {
	Session string `path:"session" doc:"WhatsApp session id. Must belong to the caller organization." example:"sess_01HZX"`
	Source  string `query:"source" doc:"Optional source filter: dm or group. Omit for all." enum:"dm,group" example:"dm"`
	Group   string `query:"group" doc:"Optional group JID filter." example:"12345-67890@g.us"`
	Q       string `query:"q" doc:"Case-insensitive search over contact name and number." example:"alice"`
	Limit   int    `query:"limit" doc:"Maximum contacts to return. Defaults to 50. Clamped to 1-200." example:"50"`
	Cursor  string `query:"cursor" doc:"Opaque pagination cursor from previous page." example:"eyJpZCI6MTAwfQ"`
}

// checkContactInput is GET /sessions/{session}/contacts/check?phone=.
type checkContactInput struct {
	Session string `path:"session" doc:"WhatsApp session id used for live lookup. Must be connected." example:"sess_01HZX"`
	Phone   string `query:"phone" doc:"Phone number in digits, optionally with +." example:"+14155550123"`
}

// getContactInput is GET /sessions/{session}/contacts/{lid}.
type getContactInput struct {
	Session string `path:"session" doc:"WhatsApp session id that owns stored contact data." example:"sess_01HZX"`
	LID     string `path:"lid" doc:"Contact LID from stored data." example:"123456789@lid"`
}

// contactJIDInput is GET /sessions/{session}/contacts/{jid}/(picture|about) and
// POST /sessions/{session}/contacts/{jid}/(block|unblock).
type contactJIDInput struct {
	Session string `path:"session" doc:"WhatsApp session id used for live actions. Must be connected." example:"sess_01HZX"`
	JID     string `path:"jid" doc:"WhatsApp JID of target user/group/channel." example:"14155550123@s.whatsapp.net"`
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

// RegisterContactOps registers contact operations.
func RegisterContactOps(api huma.API, h *Handlers) {
	read := huma.Middlewares{humax.RequireCap(api, authz.CapRead)}
	send := huma.Middlewares{humax.RequireCap(api, authz.CapSend)}

	huma.Register(api, huma.Operation{
		OperationID: "listContacts", Method: "GET", Path: "/api/v1/sessions/{session}/contacts",
		Summary: "List a session's contacts (found users)",
		Description: "Returns contacts from stored data for one session.\n\n" +
			"Optional filters: `source`, `group`, and `q`.\n\n" +
			"Requires `read`. Errors: `not_found` for bad session ownership, `validation_error` for bad query values.",
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
		Description: "Runs a live WhatsApp lookup for one phone number and returns JID if present.\n\n" +
			"Requires `read` and a connected session.\n\n" +
			"Errors: `validation_error` for bad phone, `not_found` for session ownership, `not_implemented` for disconnected session.",
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
		Description: "Returns stored data for one contact by LID.\n\n" +
			"Requires `read`.\n\n" +
			"Errors: `not_found` if session/contact is missing or inaccessible.",
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
		Description: "Fetches profile picture URL from WhatsApp for this JID.\n\n" +
			"Requires `read` and a connected session.\n\n" +
			"Errors: `not_found` if not visible, `not_implemented` if session is disconnected.",
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
		Description: "Fetches WhatsApp status text for a contact.\n\n" +
			"Requires `read` and a connected session.\n\n" +
			"Errors: `not_found` for missing access, `not_implemented` for disconnected session.",
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
		Description: "Blocks a contact for this session on WhatsApp.\n\n" +
			"Requires `send` and a connected session.\n\n" +
			"Errors: `not_found` if session/contact is inaccessible, `not_implemented` if disconnected.",
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
		Description: "Unblocks a contact for this session on WhatsApp.\n\n" +
			"Requires `send` and a connected session.\n\n" +
			"Errors: `not_found` if session/contact is inaccessible, `not_implemented` if disconnected.",
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
