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
	File huma.FormFile `form:"file" doc:"The WhatsApp end-to-end encrypted message-store backup file, named msgstore.db.crypt15 on the device. This is the raw, still-encrypted blob — do NOT decrypt it client-side. Required: the request fails with validation_error (400) if the part is absent or its body is empty. Bounded to 256 MiB; a larger upload trips the body cap and is rejected before reaching the handler."`
	Key  string        `form:"key" doc:"The crypt15 decryption key for the uploaded backup. Accepts three forms: (1) a 64-character hex string — the human-readable code shown under WhatsApp > Settings > Chats > Chat backup > End-to-end encrypted backup; (2) a raw 32-byte key; or (3) a serialized encrypted_backup.key file. Leading/trailing whitespace is trimmed. Required: an empty or whitespace-only value fails with validation_error (400). The key is used inline to decrypt the file before the job is accepted, so a wrong key fails fast with 400 rather than failing the background job." example:"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"`
}

type importBackupInput struct {
	Session string `path:"session" doc:"Identifier of the WhatsApp session whose history the backup is imported into. The caller's organization must own this session (otherwise not_found, 404)." example:"sess_01HZX9..."`
	RawBody huma.MultipartFormFiles[backfillForm]
}

type backupStatusInput struct {
	Session string `path:"session" doc:"Identifier of the WhatsApp session whose latest import job is reported. The caller's organization must own this session (otherwise not_found, 404)." example:"sess_01HZX9..."`
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
		Summary: "Import a WhatsApp backup (crypt15) to backfill history", Tags: []string{"Backup"},
		Description: `Uploads an end-to-end encrypted WhatsApp message-store backup (` + "`msgstore.db.crypt15`" + `) together with its decryption key, and backfills the session's chat history from it.

**What it does.** The gateway decrypts the uploaded blob with the supplied key, opens the embedded SQLite database, and **upserts** the session's chats, messages, WhatsApp identities, groups, and group members. The import is **idempotent by message id**: re-running the same (or an overlapping) backup merges rather than duplicates, so it is safe to retry.

**Use this** to seed or top up a freshly linked session with the history that already exists on the device — the live event stream only covers messages received after the session connected.

**Preconditions.**
- Requires the ` + "`manage`" + ` capability.
- The session must exist and be owned by the caller's organization.
- Both the ` + "`file`" + ` part (non-empty) and the ` + "`key`" + ` field (non-empty after trimming) are required.

**Decryption is inline; import is asynchronous.** The key and file are validated and the backup is decrypted **synchronously** while handling the request, so a wrong key, corrupt file, or unreadable upload fails immediately with ` + "`400`" + `. Once decryption succeeds the request returns **202 Accepted** and the actual upsert runs in the **background**. Poll ` + "`GET /api/v1/sessions/{session}/backfill`" + ` for live progress counts and the terminal outcome.

**Concurrency / idempotency.** Only one import may run per session at a time. A second import requested while one is still running is rejected with ` + "`409`" + ` (conflict). There is no ` + "`Idempotency-Key`" + ` header on this route; duplicate suppression comes from the per-session single-flight lock plus message-id upsert semantics.

**Rate limit.** Ordinary callers may import at most **once per 24 hours per session**; exceeding this returns ` + "`429`" + `. Platform ` + "`super_admin`" + ` principals are exempt and have no limit.

**Limits.** The upload is capped at **256 MiB**; a larger body is rejected by the body cap. The body-read timeout is disabled for this route because large msgstore backups can take well beyond the default read window to upload.

**Errors.**
- ` + "`validation_error`" + ` (400) — missing/empty ` + "`file`" + `, missing/empty ` + "`key`" + `, unreadable upload, wrong key, or an undecryptable/corrupt backup.
- ` + "`not_found`" + ` (404) — the session does not exist or is not owned by the caller's organization.
- ` + "`conflict`" + ` (409) — an import is already running for this session.
- ` + "`rate_limited`" + ` (429) — the 24-hour-per-session limit was hit (non-super_admin callers).`,
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
		Summary: "Get the latest backup import job for a session", Tags: []string{"Backup"}, Middlewares: manage,
		Description: `Returns the **most recent** backup import for this session — whether it is still running, has succeeded, or has failed — along with its progress counts.

**Use this** to poll the outcome of an import started via ` + "`POST /api/v1/sessions/{session}/backfill`" + `, which returns ` + "`202`" + ` and completes its work in the background.

**Preconditions.**
- Requires the ` + "`manage`" + ` capability.
- The session must exist and be owned by the caller's organization.

This is a read-only operation with no side effects; it is safe to call repeatedly. The returned job reflects the latest known state at read time.

**Errors.**
- ` + "`not_found`" + ` (404) — the session does not exist, is not owned by the caller's organization, or no import has ever been started for it.`,
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
