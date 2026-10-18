// The public API base URL a user's own code should call, for display in
// snippets and "API endpoint" cards. It is the same base the browser client
// uses (apiUrl("") → `${VITE_GATEWAY_URL}/api/v1`). When VITE_GATEWAY_URL is
// unset the API is served same-origin, so the browser's origin is prepended to
// make the URL copy-pasteable into a terminal.

import { useSyncExternalStore } from "react";
import { apiUrl } from "./client";

const noopSubscribe = () => () => {};

/** Absolute public API base (e.g. "https://wa.example.com/api/v1"), no trailing slash. */
export function publicApiBaseUrl(origin?: string): string {
  const base = apiUrl("");
  if (/^https?:\/\//i.test(base)) return base;
  const o = origin ?? (typeof window !== "undefined" ? window.location.origin : "");
  return `${o.replace(/\/+$/, "")}${base}`;
}

/** Hook form of publicApiBaseUrl() that is hydration-safe (resolves the origin on the client). */
export function usePublicApiUrl(): string {
  return useSyncExternalStore(
    noopSubscribe,
    () => publicApiBaseUrl(),
    () => publicApiBaseUrl(""),
  );
}
