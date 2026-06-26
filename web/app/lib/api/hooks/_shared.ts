// Internal helpers shared by the resource hooks. Not part of the public surface
// contract, but stable. FROZEN — owned by the foundation agent.

import { fetchJSON, apiUrl } from "../client";
import type { Page } from "../envelope";

/** Default page size for cursor lists (matches the API's default). */
export const PAGE_LIMIT = "50";

/** Build a query string from a record, skipping undefined/empty values. */
export function queryString(
  params: Record<string, string | number | boolean | undefined>,
): string {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === "") continue;
    u.set(k, String(v));
  }
  const s = u.toString();
  return s ? `?${s}` : "";
}

/**
 * Build the queryFn for a cursor-paginated list. `path` is the resource path
 * (already session-scoped if needed); `extra` carries filter params.
 */
export function listPageFetcher<T>(
  path: string,
  extra: Record<string, string | number | boolean | undefined> = {},
) {
  return ({ pageParam }: { pageParam: string | undefined }) => {
    const u = new URLSearchParams();
    u.set("limit", PAGE_LIMIT);
    for (const [k, v] of Object.entries(extra)) {
      if (v === undefined || v === "") continue;
      u.set(k, String(v));
    }
    if (pageParam) u.set("cursor", pageParam);
    return fetchJSON<Page<T>>(apiUrl(`${path}?${u.toString()}`));
  };
}

/** getNextPageParam for every cursor list. */
export function nextCursor<T>(last: Page<T>): string | undefined {
  return last.nextCursor ?? undefined;
}

export { fetchJSON, apiUrl };
