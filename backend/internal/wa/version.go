package wa

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
)

// latestVersionFetcher reads WhatsApp Web's current client version. It is a
// variable so tests can replace it.
var latestVersionFetcher = whatsmeow.GetLatestVersion

// RefreshWAVersion adopts WhatsApp Web's current client version when it is
// newer than the one compiled into whatsmeow.
//
// WhatsApp rejects clients whose version falls too far behind with
// ClientOutdated (405), and every session then fails until the gateway is
// upgraded. That took this gateway down from 2026-10-26 to 2026-11-23. Reading
// the live version at boot keeps the gateway accepted between whatsmeow
// upgrades. It cannot replace them: a protocol change still needs new code.
//
// Call it once at boot, before any session connects: store.SetWAVersion writes
// a package global that connecting clients read without a lock. A failed fetch
// keeps the compiled version.
func RefreshWAVersion(ctx context.Context, log *slog.Logger, httpClient *http.Client) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	compiled := store.GetWAVersion()
	latest, err := latestVersionFetcher(ctx, httpClient)
	if err != nil || latest == nil {
		log.Warn("could not read the current WhatsApp Web version; using the compiled one",
			"compiled", compiled.String(), "err", err)
		return
	}
	if !newerVersion(*latest, compiled) {
		log.Info("whatsapp web version is current", "version", compiled.String())
		return
	}
	store.SetWAVersion(*latest)
	log.Info("adopted the current WhatsApp Web version", "compiled", compiled.String(), "version", latest.String())
}

// newerVersion reports whether a is a later version than b.
func newerVersion(a, b store.WAVersionContainer) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
