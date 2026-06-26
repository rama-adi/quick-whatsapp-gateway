// Local helpers for the auth surface. Surface-private (owned by the auth agent).
//
// NOTE (sharedGaps): the foundation's ~/lib/auth/client exposes signIn/signUp as
// `Promise<T>` (generic) and a fixed totpVerify({code}) that cannot pass
// `trust_device`. To support the "remember this device" affordance and to type
// the Authula sign-in response (which can be EITHER {user,session} OR
// {totp_redirect:true}) we call authFetch directly here. If the verify phase
// wants to hoist these typed wrappers + the post-auth redirect helper into
// ~/lib/auth/client, they live here for now.

import { authFetch } from "~/lib/auth/client";
import { loadSession } from "~/lib/auth/session";

/** Authula user object (subset we rely on). */
export interface AuthulaUser {
  id: string;
  email: string;
  name?: string;
}

/** Authula session object (subset). */
export interface AuthulaSession {
  id: string;
  userId?: string;
  expiresAt?: string;
}

/** Successful sign-in / verify / sign-up response. */
export interface AuthSuccessResponse {
  user: AuthulaUser;
  session: AuthulaSession;
}

/** Returned by sign-in when TOTP is enabled: no session is minted yet. */
export interface TotpRedirectResponse {
  totp_redirect: true;
}

export type SignInResponse = AuthSuccessResponse | TotpRedirectResponse;

export function isTotpRedirect(r: SignInResponse): r is TotpRedirectResponse {
  return (r as TotpRedirectResponse).totp_redirect === true;
}

export interface SignInInput {
  email: string;
  password: string;
}
export interface SignUpInput {
  name: string;
  email: string;
  password: string;
}
export interface VerifyTotpInput {
  code: string;
  trustDevice?: boolean;
}

/** POST /auth/email-password/sign-in — may resolve to a TOTP challenge. */
export function signInRequest(body: SignInInput): Promise<SignInResponse> {
  return authFetch<SignInResponse>("/email-password/sign-in", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

/** POST /auth/email-password/sign-up — auto sign-in mints a session cookie. */
export function signUpRequest(body: SignUpInput): Promise<AuthSuccessResponse> {
  return authFetch<AuthSuccessResponse>("/email-password/sign-up", {
    method: "POST",
    body: JSON.stringify({
      name: body.name,
      email: body.email,
      password: body.password,
    }),
  });
}

/** POST /auth/totp/verify — completes the 2FA challenge using the pending cookie. */
export function verifyTotpRequest(body: VerifyTotpInput): Promise<AuthSuccessResponse> {
  return authFetch<AuthSuccessResponse>("/totp/verify", {
    method: "POST",
    body: JSON.stringify({ code: body.code, trust_device: body.trustDevice ?? false }),
  });
}

/** POST /auth/totp/verify-backup-code — fallback when the authenticator is lost. */
export function verifyBackupCodeRequest(body: VerifyTotpInput): Promise<AuthSuccessResponse> {
  return authFetch<AuthSuccessResponse>("/totp/verify-backup-code", {
    method: "POST",
    body: JSON.stringify({ code: body.code, trust_device: body.trustDevice ?? false }),
  });
}

/**
 * Resolve the landing route after a successful authentication, mirroring
 * routes/home.tsx role routing without depending on it. Probes role +
 * userPanelEnabled via loadSession(); falls back to "/".
 */
export async function resolvePostAuthRedirect(): Promise<string> {
  const session = await loadSession();
  if (!session) return "/login";
  if (session.user.roles.includes("super_admin")) return "/admin/sessions";
  if (session.userPanelEnabled && session.user.roles.includes("user")) {
    return "/user/sessions";
  }
  return "/";
}

/**
 * Validate a "next" query param so we never open-redirect off-site. Only
 * same-origin absolute paths are honored.
 */
export function safeNext(next: string | null): string | null {
  if (!next) return null;
  if (!next.startsWith("/") || next.startsWith("//")) return null;
  return next;
}
