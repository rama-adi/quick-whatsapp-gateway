// Package static embeds the built React Router SPA so the Go binary can serve
// the dashboard and the JSON API from a single port.
package static

import "embed"

// Assets holds the compiled SPA build (web/build/client copied here at image
// build time). In development the dist/ directory contains only a placeholder
// index.html; the production Dockerfile overwrites it with the real build.
//
//go:embed dist
var Assets embed.FS
