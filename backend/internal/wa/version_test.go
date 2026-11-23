package wa

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"go.mau.fi/whatsmeow/store"
)

func TestRefreshWAVersion(t *testing.T) {
	compiled := store.GetWAVersion()
	t.Cleanup(func() { store.SetWAVersion(compiled) })
	original := latestVersionFetcher
	t.Cleanup(func() { latestVersionFetcher = original })

	newer := store.WAVersionContainer{compiled[0], compiled[1], compiled[2] + 1}
	older := store.WAVersionContainer{compiled[0], compiled[1], compiled[2] - 1}

	latestVersionFetcher = func(context.Context, *http.Client) (*store.WAVersionContainer, error) {
		return &older, nil
	}
	RefreshWAVersion(context.Background(), quietLogger(), nil)
	if got := store.GetWAVersion(); got != compiled {
		t.Fatalf("older live version replaced the compiled one: %v", got)
	}

	latestVersionFetcher = func(context.Context, *http.Client) (*store.WAVersionContainer, error) {
		return nil, errors.New("offline")
	}
	RefreshWAVersion(context.Background(), quietLogger(), nil)
	if got := store.GetWAVersion(); got != compiled {
		t.Fatalf("failed fetch changed the version: %v", got)
	}

	latestVersionFetcher = func(context.Context, *http.Client) (*store.WAVersionContainer, error) {
		return &newer, nil
	}
	RefreshWAVersion(context.Background(), quietLogger(), nil)
	if got := store.GetWAVersion(); got != newer {
		t.Fatalf("version = %v, want the newer live version %v", got, newer)
	}
}
