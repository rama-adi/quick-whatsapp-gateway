import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

// Resolve the "~/*" alias for tests without vite-tsconfig-paths (which collides
// with the vite version vitest bundles). Keep this in sync with tsconfig paths.
const appDir = fileURLToPath(new URL("./app", import.meta.url));

export default defineConfig({
  resolve: {
    alias: { "~": appDir },
  },
  test: {
    environment: "node",
    include: ["app/**/*.test.ts"],
  },
});
