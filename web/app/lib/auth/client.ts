// Authula auth client. Authula serves auth under /auth/* (NOT /api/v1) and uses
// a cookie session + double-submit CSRF token.
// FROZEN — owned by the foundation agent.
//
// CSRF (recon §1): cookie name "authula_csrf_token", header
// "X-AUTHULA-CSRF-TOKEN". The token is mirrored from the cookie into the header
// on every state-changing request.

import { ApiError, parseError } from "../api/envelope";

const AUTH_BASE = "/auth";
const CSRF_COOKIE = "authula_csrf_token";
const CSRF_HEADER = "X-AUTHULA-CSRF-TOKEN";

export function authUrl(path: string): string {
  return `${AUTH_BASE}${path}`;
}

function readCookie(name: string): string | undefined {
  if (typeof document === "undefined") return undefined;
  const match = document.cookie
    .split("; ")
    .find((row) => row.startsWith(`${name}=`));
  return match ? decodeURIComponent(match.slice(name.length + 1)) : undefined;
}

const STATE_CHANGING = new Set(["POST", "PUT", "PATCH", "DELETE"]);

/**
 * Fetch against the Authula surface. Adds credentials + CSRF header and maps
 * non-2xx responses into ApiError. Returns undefined for 204/no-content.
 */
export async function authFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const method = (init?.method ?? "GET").toUpperCase();
  const headers = new Headers(init?.headers);
  if (init?.body !== undefined && init.body !== null && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (!headers.has("Accept")) headers.set("Accept", "application/json");
  if (STATE_CHANGING.has(method)) {
    const token = readCookie(CSRF_COOKIE);
    if (token) headers.set(CSRF_HEADER, token);
  }

  const res = await fetch(authUrl(path), {
    ...init,
    method,
    headers,
    credentials: "include",
  });

  if (res.status === 204 || res.status === 205) return undefined as T;

  const text = await res.text();
  let parsed: unknown = undefined;
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = undefined;
    }
  }

  if (!res.ok) throw parseError(parsed, res.status);
  return parsed as T;
}

// --- Convenience wrappers for the common flows (routes per recon §6). ---

export interface SignInBody {
  email: string;
  password: string;
}
export interface SignUpBody {
  email: string;
  password: string;
  name?: string;
}

export function signIn<T = unknown>(body: SignInBody): Promise<T> {
  return authFetch<T>("/email-password/sign-in", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function signUp<T = unknown>(body: SignUpBody): Promise<T> {
  return authFetch<T>("/email-password/sign-up", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function signOut(): Promise<void> {
  return authFetch<void>("/sign-out", { method: "POST" });
}

export function totpVerify<T = unknown>(body: { code: string }): Promise<T> {
  return authFetch<T>("/totp/verify", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export { ApiError };
