// Surface-local helpers for the AUTH surface (login / register / 2fa).
// Colocated and surface-private (the leading "-" keeps this file OUT of the
// TanStack Router route tree — it is not a route, just a helper module).
//
// What lives here:
//   - Sign-in / sign-up / 2fa go straight through the better-auth client in the
//     route components (authClient.signIn.email / signUp.email /
//     twoFactor.verifyTotp / verifyBackupCode) — its React client already
//     returns a typed { data, error } envelope, so no wrapper is needed here.
//   - resolvePostAuthRedirect is a SERVER FN (it needs the better-auth session
//     cookie + a Drizzle member-role read), reusing getServerSession from
//     ~/lib/auth/session.
//   - safeNext is the open-redirect guard — pure + client-safe.
//   - registrationEnabled is a server fn reading USER_REGISTRATION_ENABLED
//     (§12 gate).

import { createServerFn } from "@tanstack/react-start";
import { getServerSession } from "~/lib/auth/session";

/**
 * Resolve the landing route after a successful authentication, by role (§12 /
 * Stage-3 spec): super_admin -> /admin/sessions, user -> /user/sessions.
 * Runs on the server (reads the better-auth session cookie). Returns "/login"
 * when there is no session yet (caller should stay on the form).
 */
export const resolvePostAuthRedirect = createServerFn({ method: "GET" }).handler(
  async (): Promise<string> => {
    const session = await getServerSession();
    if (!session) return "/login";
    if (session.user.roles.includes("super_admin")) return "/admin/sessions";
    if (session.userPanelEnabled && session.user.roles.includes("user")) {
      return "/user/sessions";
    }
    return "/";
  },
);

/**
 * Whether self-registration is enabled (§12, §14 — USER_REGISTRATION_ENABLED).
 * Mirrors the server's emailAndPassword.disableSignUp gate so the UI can hide
 * the register link / redirect /register -> /login when locked down. Replaces
 * the v1 OPTIONS-probe of /auth/email-password/sign-up.
 */
export const registrationEnabled = createServerFn({ method: "GET" }).handler(
  async (): Promise<boolean> => {
    return process.env.USER_REGISTRATION_ENABLED !== "false";
  },
);

/**
 * Validate a "next" query param so we never open-redirect off-site. Only
 * same-origin absolute paths are honored. Pure + client-safe (kept from v1).
 */
export function safeNext(next: string | null | undefined): string | null {
  if (!next) return null;
  if (!next.startsWith("/") || next.startsWith("//")) return null;
  return next;
}

/** Map a better-auth client error into a friendly sign-in message. */
export function signInErrorMessage(
  error: { status?: number; code?: string; message?: string } | null,
): string {
  if (!error) return "Sign in failed.";
  if (error.status === 401 || error.code === "INVALID_EMAIL_OR_PASSWORD") {
    return "Incorrect email or password.";
  }
  if (error.status === 429 || error.code === "TOO_MANY_REQUESTS") {
    return "Too many attempts. Please wait and try again.";
  }
  return error.message || "Sign in failed.";
}

/** Map a better-auth client error into a friendly sign-up message. */
export function signUpErrorMessage(
  error: { status?: number; code?: string; message?: string } | null,
): string {
  if (!error) return "Sign up failed.";
  if (error.status === 422 || error.code === "USER_ALREADY_EXISTS") {
    return "An account with that email already exists.";
  }
  if (error.status === 429) {
    return "Too many attempts. Please wait and try again.";
  }
  if (error.status === 403) {
    return "Self-registration is currently disabled.";
  }
  return error.message || "Sign up failed.";
}

/** Map a better-auth client error into a friendly 2FA verify message. */
export function verifyErrorMessage(
  error: { status?: number; code?: string; message?: string } | null,
  mode: "authenticator" | "backup",
): string {
  if (!error) return "Verification failed.";
  if (error.status === 401 || error.status === 400) {
    return mode === "authenticator"
      ? "That code is incorrect or expired. Try again."
      : "That backup code is invalid or already used.";
  }
  if (error.status === 429) {
    return "Too many attempts. Please wait and try again.";
  }
  return error.message || "Verification failed.";
}
