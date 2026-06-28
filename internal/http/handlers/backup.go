package handlers

import (
	"io"
	"net/http"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// maxBackupUpload caps the uploaded .crypt15 size (msgstore backups are large but
// bounded). The +1MB slack lets MaxBytesReader trip cleanly past the cap.
const maxBackupUpload = 256 << 20 // 256 MiB

// ImportBackup handles POST /sessions/{session}/backfill — a multipart upload of a
// WhatsApp msgstore.db.crypt15 (`file`) plus its decryption key (`key`). Starts an
// async import job and returns it (202).
func (h *Handlers) ImportBackup(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBackupUpload+(1<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		httpx.WriteError(w, domain.ErrValidation("invalid or too-large upload: "+err.Error()))
		return
	}

	key := strings.TrimSpace(r.FormValue("key"))
	if key == "" {
		httpx.WriteError(w, domain.ErrValidation("key is required"))
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, domain.ErrValidation("file is required"))
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		httpx.WriteError(w, domain.ErrValidation("could not read upload: "+err.Error()))
		return
	}
	if len(data) == 0 {
		httpx.WriteError(w, domain.ErrValidation("file is empty"))
		return
	}

	job, err := h.Backup.StartImport(r.Context(), organizationID, param(r, "session"), isSuperAdmin(r), data, key)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, job)
}

// BackupStatus handles GET /sessions/{session}/backfill — the latest import job
// for the session.
func (h *Handlers) BackupStatus(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := organization(w, r)
	if !ok {
		return
	}
	job, err := h.Backup.ImportStatus(r.Context(), organizationID, param(r, "session"), isSuperAdmin(r))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, job)
}

// isSuperAdmin reports whether the request's verified principal is a platform
// super_admin (the quota/ownership bypass for backup imports).
func isSuperAdmin(r *http.Request) bool {
	p := authz.FromContext(r.Context())
	return p != nil && p.IsSuperAdmin()
}
