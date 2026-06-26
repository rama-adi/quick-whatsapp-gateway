// Typed fetch wrapper for the JSON API under /api/v1.
// FROZEN — owned by the foundation agent. Surface agents import, never edit.
//
// Every call uses credentials:"include" so the Authula cookie session rides
// along (the dashboard never sends an API key). Errors are normalized into the
// shared ApiError; 204/empty responses resolve to undefined.

import { ApiError, parseError } from "./envelope";

const API_BASE = "/api/v1";

/** Build a fully-qualified API path. Pass a path starting with "/". */
export function apiUrl(path: string): string {
  return `${API_BASE}${path}`;
}

/**
 * Perform a JSON request against the API. Resolves to the parsed body typed as
 * T (or undefined for 204/no-content). Throws ApiError on any non-2xx.
 *
 * Pass `input` already built with apiUrl(...) — this does NOT prefix the base,
 * so the same helper can hit absolute URLs if ever needed.
 */
export async function fetchJSON<T>(input: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  // Only set a JSON content type when there's a body to send.
  if (init?.body !== undefined && init.body !== null && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (!headers.has("Accept")) {
    headers.set("Accept", "application/json");
  }

  const res = await fetch(input, {
    ...init,
    headers,
    credentials: "include",
  });

  if (res.status === 204 || res.status === 205) {
    return undefined as T;
  }

  // Read the body once; reuse for both success and error paths.
  const text = await res.text();
  const parsed: unknown = text ? safeJsonParse(text) : undefined;

  if (!res.ok) {
    throw parseError(parsed, res.status);
  }

  return parsed as T;
}

function safeJsonParse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

export { ApiError };
