import type { Config } from "@react-router/dev/config";

// SPA mode: no SSR. The Go binary serves the static build with an index.html
// fallback for client-side routing. Build output lands at build/client, which
// the Dockerfile copies to internal/http/static/dist.
export default {
  ssr: false,
  buildDirectory: "build",
} satisfies Config;
