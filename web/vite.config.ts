import { defineConfig } from "vite";
import { devtools } from "@tanstack/devtools-vite";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import { nitro } from "nitro/vite";
import viteReact from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// TanStack Start integrates via the Vite plugin (verified against the installed
// @tanstack/react-start@1.168 — the older vinxi/app.config.ts path is gone).
// srcDirectory keeps our routes under ./app; the router config points the
// generator at ./app/routes and the generated tree at ./app/routeTree.gen.ts.
export default defineConfig({
  // Vite 8 resolves tsconfig `paths` (the "~/*" -> ./app/* alias) natively
  // across every environment the Start plugin spins up (client + ssr + server).
  resolve: { tsconfigPaths: true },
  plugins: [
    devtools(),
    tailwindcss(),
    nitro(),
    tanstackStart({
      srcDirectory: "app",
      router: {
        routesDirectory: "routes",
        generatedRouteTree: "routeTree.gen.ts",
      },
    }),
    viteReact(),
  ],
  server: {
    // better-auth mounts on THIS frontend at /api/auth/* (same origin) — no
    // proxy needed for it. The gateway is a SEPARATE origin reached directly
    // by the browser via GATEWAY_URL (CORS + Bearer JWT), so it is NOT proxied
    // here either. This block is intentionally empty of route proxies.
    port: 3000,
  },
});
