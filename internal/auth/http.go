package auth

import (
	"encoding/json"
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// writeAuthError emits the §11 error envelope for the RequireRole/RequirePermission
// guards. Codes map to the domain sentinels so the gateway's error shape is
// consistent across the HTTP edge.
func writeAuthError(w http.ResponseWriter, status int, message string) {
	var code string
	switch status {
	case http.StatusUnauthorized:
		code = domain.CodeUnauthorized
	case http.StatusForbidden:
		code = domain.CodeForbidden
	default:
		code = domain.CodeInternal
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(domain.ErrorBody{Error: domain.NewAPIError(code, message)})
}
