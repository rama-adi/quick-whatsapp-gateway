package handlers

import (
	"context"
	"io"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
)

// maxBackupUpload caps the uploaded .crypt15 size (msgstore backups are large but
// bounded). The +1MB slack lets the body cap trip cleanly past the limit.
const maxBackupUpload = 256 << 20 // 256 MiB

// backfillForm is the multipart payload for POST /sessions/{session}/backfill: a
// WhatsApp msgstore.db.crypt15 (`file`) plus its decryption key (`key`). Fields are
// left OPTIONAL on the wire (no `required:"true"`) so the service stays the single
// validator — the handler maps missing key/empty file to domain.ErrValidation (400),
// matching the old chi handler's §11 validation_error envelope.
type backfillForm struct {
	File huma.FormFile `form:"file"`
	Key  string        `form:"key"`
}

type importBackupInput struct {
	Session string `path:"session"`
	RawBody huma.MultipartFormFiles[backfillForm]
}

type backupStatusInput struct {
	Session string `path:"session"`
}

type backupOutput struct{ Body domain.BackfillImport }

// RegisterBackupOps registers the backup (crypt15) import operations (manage
// capability) on the huma API. This is the code-first replacement for the chi
// /backfill routes. The upload is a multipart form; the §11 validation of the
// key/file remains in the handler/service so the wire contract is unchanged.
func RegisterBackupOps(api huma.API, h *Handlers) {
	manage := huma.Middlewares{humax.RequireCap(api, authz.CapManage)}

	huma.Register(api, huma.Operation{
		OperationID: "importBackup", Method: "POST", Path: "/api/v1/sessions/{session}/backfill",
		Summary: "Import a WhatsApp backup (crypt15)", Tags: []string{"Backup"},
		DefaultStatus: 202, Middlewares: manage,
		// Cap the upload at 256 MiB (+1 MiB slack) and disable the body-read timeout —
		// large msgstore backups need more than huma's 5s default.
		MaxBodyBytes:    maxBackupUpload + (1 << 20),
		BodyReadTimeout: -1,
	}, func(ctx context.Context, in *importBackupInput) (*backupOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		p, err := humax.Principal(ctx)
		if err != nil {
			return nil, err
		}

		form := in.RawBody.Data()

		key := strings.TrimSpace(form.Key)
		if key == "" {
			return nil, humax.Err(domain.ErrValidation("key is required"))
		}

		if !form.File.IsSet {
			return nil, humax.Err(domain.ErrValidation("file is required"))
		}
		data, err := io.ReadAll(form.File)
		if err != nil {
			return nil, humax.Err(domain.ErrValidation("could not read upload: " + err.Error()))
		}
		if len(data) == 0 {
			return nil, humax.Err(domain.ErrValidation("file is empty"))
		}

		job, err := h.Backup.StartImport(ctx, org, in.Session, p.IsSuperAdmin(), data, key)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &backupOutput{Body: job}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "backupStatus", Method: "GET", Path: "/api/v1/sessions/{session}/backfill",
		Summary: "Latest backup import status", Tags: []string{"Backup"}, Middlewares: manage,
	}, func(ctx context.Context, in *backupStatusInput) (*backupOutput, error) {
		org, err := humax.Org(ctx)
		if err != nil {
			return nil, err
		}
		p, err := humax.Principal(ctx)
		if err != nil {
			return nil, err
		}
		job, err := h.Backup.ImportStatus(ctx, org, in.Session, p.IsSuperAdmin())
		if err != nil {
			return nil, humax.Err(err)
		}
		return &backupOutput{Body: job}, nil
	})
}
