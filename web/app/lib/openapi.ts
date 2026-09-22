// Both the generated reference and its runtime payload use the user audience.
// The full API contract remains in docs/openapi.yaml for developer documentation.
import { createOpenAPI } from "fumadocs-openapi/server";

const full = createOpenAPI({ input: ["../docs/openapi.yaml"] });

export const openapi = createOpenAPI({
  input: {
    "../docs/openapi.yaml": async () => {
      const { bundled } = await full.getSchema("../docs/openapi.yaml");
      return {
        ...bundled,
        paths: Object.fromEntries(
          Object.entries(bundled.paths ?? {}).filter(
            ([path]) => !/^\/(?:api\/v1\/)?admin(?:\/|$)/.test(path),
          ),
        ),
        tags: bundled.tags?.filter(
          (tag) => !["Admin", "Gateway Administration"].includes(tag.name ?? ""),
        ),
      };
    },
  },
});
