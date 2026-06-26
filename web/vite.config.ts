import { defineConfig } from "vite";
import { devtools } from "@tanstack/devtools-vite";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import { nitro } from "nitro/vite";
import viteReact from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import mdx from "fumadocs-mdx/vite";
import { createRequire } from "node:module";

// Resolve tslib's ESM build to an absolute path. fumadocs-openapi's
// @fumadocs/api-docs chunk reads tslib's `__extends` helper; the default
// (UMD) tslib entry assigns its helpers to globalThis when bundled, leaving
// the module's exports empty, so the SSR server throws "Cannot destructure
// property '__extends'". The ESM build exports the helpers as named bindings.
const require = createRequire(import.meta.url);
const tslibEsm = require.resolve("tslib/tslib.es6.mjs");

// TanStack Start integrates via the Vite plugin (verified against the installed
// @tanstack/react-start@1.168 — the older vinxi/app.config.ts path is gone).
// srcDirectory keeps our routes under ./app; the router config points the
// generator at ./app/routes and the generated tree at ./app/routeTree.gen.ts.
export default defineConfig({
  // Vite 8 resolves tsconfig `paths` (the "~/*" -> ./app/* alias) natively
  // across every environment the Start plugin spins up (client + ssr + server).
  // The tslib alias (see tslibEsm above) points every tslib import at the ESM
  // build so fumadocs-openapi's helpers resolve under SSR.
  resolve: {
    tsconfigPaths: true,
    alias: { tslib: tslibEsm },
  },
  plugins: [
    // fumadocs-mdx compiles content/docs into the `collections/*` virtual
    // modules; it must run before the Start plugin so those imports resolve.
    mdx(),
    devtools(),
    tailwindcss(),
    nitro({
      // Bundle tslib (the alias points it at the ESM build) instead of tracing
      // it as an external dependency — the externals tracer otherwise
      // misreads the aliased path.
      noExternals: ["tslib"],
    }),
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
