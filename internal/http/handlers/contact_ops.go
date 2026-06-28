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
	Session string `path:"session"`
	Source  string `query:"source"`
	Group   string `query:"group"`
	Q       string `query:"q"`
	Limit   int    `query:"limit"`
	Cursor  string `query:"cursor"`
}

// checkContactInput is GET /sessions/{session}/contacts/check?phone=.
type checkContactInput struct {
	Session string `path:"session"`
	Phone   string `query:"phone"`
}

// getContactInput is GET /sessions/{session}/contacts/{lid}.
type getContactInput struct {
	Session string `path:"session"`
	LID     string `path:"lid"`
}

// contactJIDInput is GET /sessions/{session}/contacts/{jid}/(picture|about) and
// POST /sessions/{session}/contacts/{jid}/(block|unblock).
type contactJIDInput struct {
	Session string `path:"session"`
	JID     string `path:"jid"`
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
		Summary: "List contacts (found users)", Tags: []string{"Contacts"}, Middlewares: read,
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
		Summary: "Check if a phone is on WhatsApp", Tags: []string{"Contacts"}, Middlewares: read,
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
		Summary: "Get a contact", Tags: []string{"Contacts"}, Middlewares: read,
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
		Summary: "Get a contact's profile picture", Tags: []string{"Contacts"}, Middlewares: read,
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
		Summary: "Get a contact's about text", Tags: []string{"Contacts"}, Middlewares: read,
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
		Summary: "Block a contact", Tags: []string{"Contacts"},
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
		Summary: "Unblock a contact", Tags: []string{"Contacts"},
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
