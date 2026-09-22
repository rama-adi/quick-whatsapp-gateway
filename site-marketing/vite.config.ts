import { defineConfig } from 'vite';
import { tanstackStart } from '@tanstack/react-start/plugin/vite';
import { nitro } from 'nitro/vite';
import viteReact from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import mdx from 'fumadocs-mdx/vite';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);

export default defineConfig({
  resolve: {
    tsconfigPaths: true,
    // OpenAPI's generated helpers need tslib's ESM exports during SSR.
    alias: { tslib: require.resolve('tslib/tslib.es6.mjs') },
  },
  plugins: [
    mdx(),
    tailwindcss(),
    nitro({ noExternals: ['tslib'] }),
    tanstackStart({
      srcDirectory: 'app',
      router: { routesDirectory: 'routes', generatedRouteTree: 'routeTree.gen.ts' },
    }),
    viteReact(),
  ],
});
