// Docs search endpoint. Orama indexes the docs content from `source`; the
// RootProvider's search dialog queries this route.
import { createFileRoute } from "@tanstack/react-router";
import { source } from "~/lib/source";
import { createFromSource } from "fumadocs-core/search/server";

const server = createFromSource(source, {
  // https://docs.orama.com/docs/orama-js/supported-languages
  language: "english",
});

export const Route = createFileRoute("/api/search")({
  server: {
    handlers: {
      GET: async ({ request }) => server.GET(request),
    },
  },
});
