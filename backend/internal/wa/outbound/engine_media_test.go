package outbound

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func TestPrepareEngineMedia(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("image bytes"))
	}))
	defer server.Close()
	for _, input := range []string{server.URL, "data:image/png;base64,aW1hZ2UgYnl0ZXM="} {
		t.Run(input, func(t *testing.T) {
			media := &domain.MediaPayload{Caption: "caption", Filename: "image.png"}
			if input == server.URL {
				media.URL = input
			} else {
				media.Data = input
			}
			original := *media
			got, err := PrepareEngineMedia(context.Background(), domain.SendRequest{Type: domain.SendTypeImage, To: "123@s.whatsapp.net", Media: media})
			if err != nil {
				t.Fatal(err)
			}
			data, err := base64.StdEncoding.DecodeString(got.Media.Data)
			if err != nil || string(data) != "image bytes" || got.Media.URL != "" || got.Media.Mimetype != "image/png" {
				t.Fatalf("media = %+v, error = %v", got.Media, err)
			}
			if *media != original || got.Media.Caption != media.Caption || got.Media.Filename != media.Filename {
				t.Fatal("metadata or original input changed")
			}
		})
	}
	req := domain.SendRequest{Type: domain.SendTypeAlbum, To: "123@s.whatsapp.net", Medias: []domain.AlbumMediaPayload{{URL: server.URL}, {Data: "aW1hZ2UgYnl0ZXM="}}}
	got, err := PrepareEngineMedia(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Medias[0].URL != "" || got.Medias[0].Data != got.Medias[1].Data || req.Medias[0].URL != server.URL {
		t.Fatalf("album = %+v", got)
	}
}

func TestPrepareEngineMediaDownloadFailure(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	_, err := PrepareEngineMedia(context.Background(), domain.SendRequest{Type: domain.SendTypeImage, To: "123@s.whatsapp.net", Media: &domain.MediaPayload{URL: server.URL}})
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeValidationError {
		t.Fatalf("error = %v", err)
	}
}
