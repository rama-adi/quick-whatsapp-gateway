package handlers

import (
	"context"
	"io"
	"mime"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/authz"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/humax"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/media"
)

type bucketOutput struct{ Body media.Bucket }
type bucketListOutput struct {
	Body struct {
		Items []media.Bucket `json:"items"`
	}
}
type bucketIDInput struct {
	ID string `path:"id"`
}
type bucketCreateInput struct{ Body media.BucketInput }
type bucketUpdateInput struct {
	ID   string `path:"id"`
	Body media.BucketInput
}
type bindingInput struct {
	Session string `path:"session"`
}
type bindingUpdateInput struct {
	Session string `path:"session"`
	Body    struct {
		BucketID *string `json:"bucketId" doc:"Storage connection id, or null to stop capturing new attachments."`
	}
}
type bindingOutput struct {
	Body struct {
		BucketID *string `json:"bucketId"`
	}
}
type assetOutput struct{ Body media.Asset }

func RegisterMediaOps(api huma.API, h *Handlers) {
	manage := huma.Middlewares{humax.RequireCap(api, authz.CapManage)}
	read := huma.Middlewares{humax.RequireCap(api, authz.CapRead)}
	op := func(id, method, path, summary string, m huma.Middlewares) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: "/api/v1" + path, Summary: summary, Tags: []string{"Media storage"}, Middlewares: m}
	}
	huma.Register(api, op("listStorageBuckets", "GET", "/storage/buckets", "List organization S3 connections", manage), func(ctx context.Context, _ *struct{}) (*bucketListOutput, error) {
		org, e := humax.Org(ctx)
		if e != nil {
			return nil, e
		}
		items, e := h.Media.List(ctx, org)
		if e != nil {
			return nil, humax.ErrContext(ctx, e)
		}
		out := &bucketListOutput{}
		out.Body.Items = items
		return out, nil
	})
	huma.Register(api, op("createStorageBucket", "POST", "/storage/buckets", "Connect an S3-compatible bucket", manage), func(ctx context.Context, in *bucketCreateInput) (*bucketOutput, error) {
		org, e := humax.Org(ctx)
		if e != nil {
			return nil, e
		}
		b, e := h.Media.Save(ctx, org, "", in.Body)
		if e != nil {
			return nil, humax.ErrContext(ctx, e)
		}
		return &bucketOutput{Body: b}, nil
	})
	huma.Register(api, op("updateStorageBucket", "PUT", "/storage/buckets/{id}", "Update bucket credentials and future attachment retention", manage), func(ctx context.Context, in *bucketUpdateInput) (*bucketOutput, error) {
		org, e := humax.Org(ctx)
		if e != nil {
			return nil, e
		}
		b, e := h.Media.Save(ctx, org, in.ID, in.Body)
		if e != nil {
			return nil, humax.ErrContext(ctx, e)
		}
		return &bucketOutput{Body: b}, nil
	})
	huma.Register(api, op("deleteStorageBucket", "DELETE", "/storage/buckets/{id}", "Remove an unused S3 connection", manage), func(ctx context.Context, in *bucketIDInput) (*struct{}, error) {
		org, e := humax.Org(ctx)
		if e != nil {
			return nil, e
		}
		if e := h.Media.Delete(ctx, org, in.ID); e != nil {
			return nil, humax.ErrContext(ctx, e)
		}
		return nil, nil
	})
	huma.Register(api, op("getSessionStorage", "GET", "/sessions/{session}/storage", "Get the session attachment bucket", read), func(ctx context.Context, in *bindingInput) (*bindingOutput, error) {
		org, e := humax.Org(ctx)
		if e != nil {
			return nil, e
		}
		id, e := h.Media.Binding(ctx, org, in.Session)
		if e != nil {
			return nil, humax.ErrContext(ctx, e)
		}
		out := &bindingOutput{}
		out.Body.BucketID = id
		return out, nil
	})
	huma.Register(api, op("setSessionStorage", "PUT", "/sessions/{session}/storage", "Link or unlink the session attachment bucket", manage), func(ctx context.Context, in *bindingUpdateInput) (*bindingOutput, error) {
		org, e := humax.Org(ctx)
		if e != nil {
			return nil, e
		}
		e = h.Media.Link(ctx, org, in.Session, in.Body.BucketID)
		if e != nil {
			return nil, humax.ErrContext(ctx, e)
		}
		out := &bindingOutput{}
		out.Body.BucketID = in.Body.BucketID
		return out, nil
	})
	huma.Register(api, op("getMedia", "GET", "/media/{id}", "Get attachment status and available download URL", read), func(ctx context.Context, in *bucketIDInput) (*assetOutput, error) {
		org, e := humax.Org(ctx)
		if e != nil {
			return nil, e
		}
		a, e := h.Media.Get(ctx, org, in.ID)
		if e != nil {
			return nil, humax.ErrContext(ctx, e)
		}
		return &assetOutput{Body: a}, nil
	})
}

type mediaContentInput struct {
	ID    string `path:"id"`
	Token string `query:"token" doc:"Attachment bearer token supplied in the media URL."`
}
type mediaContentOutput struct{ Body func(huma.Context) }

func RegisterMediaContentOps(api huma.API, h *Handlers) {
	huma.Register(api, huma.Operation{OperationID: "downloadMedia", Method: "GET", Path: "/api/v1/media/{id}/content", Summary: "Download an attachment using its access URL", Description: "The URL token grants access until retention expires. Keep the URL private. Every request checks the attachment state before reading its private S3 object.", Tags: []string{"Media storage"}, Security: []map[string][]string{}}, func(ctx context.Context, in *mediaContentInput) (*mediaContentOutput, error) {
		reader, a, e := h.Media.Open(ctx, in.ID, in.Token)
		if e != nil {
			return nil, humax.ErrContext(ctx, e)
		}
		return &mediaContentOutput{Body: func(c huma.Context) {
			defer reader.Close()
			c.SetHeader("Cache-Control", "private, no-store")
			c.SetHeader("Referrer-Policy", "no-referrer")
			c.SetHeader("X-Content-Type-Options", "nosniff")
			c.SetHeader("Content-Security-Policy", "sandbox")
			c.SetHeader("Content-Type", "application/octet-stream")
			c.SetHeader("Content-Length", strconv.FormatInt(a.Size, 10))
			c.SetHeader("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Filename}))
			c.SetStatus(200)
			_, _ = io.Copy(c.BodyWriter(), reader)
		}}, nil
	})
}
