import { reactRouter } from "@react-router/dev/vite";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [tailwindcss(), reactRouter()],
  // Vite 8 resolves tsconfig `paths` (the "~/*" alias) natively.
  resolve: {
    tsconfigPaths: true,
  },
  server: {
    // Same-origin proxy in dev so the cookie session + NDJSON stream work
    // against the Go backend on :8080 without CORS.
    proxy: {
      "/api": { target: "http://localhost:8080", changeOrigin: true },
      "/auth": { target: "http://localhost:8080", changeOrigin: true },
    },
  },
});
