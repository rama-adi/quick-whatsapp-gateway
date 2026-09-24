package outbound

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func TestPrepareEngineMediaDownloadFailure(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	_, err := PrepareEngineMedia(context.Background(), domain.SendRequest{Type: domain.SendTypeImage, To: "123@s.whatsapp.net", Media: &domain.MediaPayload{URL: server.URL}})
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
		t.Fatalf("error = %v", err)
	}
}
