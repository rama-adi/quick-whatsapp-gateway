// App session resolution + route guards.
// FROZEN — owned by the foundation agent. Surface agents call loadSession /
// requireSession / requireRole; they never edit this file.
//
// IMPORTANT (verified): there is NO /api/v1/me and NO config endpoint, and
// /sessions/{session}/me is the WhatsApp own-profile, NOT the actor/role. So
// role + userPanelEnabled are DERIVED here, isolated to this file, via a
// capability probe. If the backend later adds GET /api/v1/me returning
// {user,userPanelEnabled,impersonating}, swap loadSession() to read it — no
// surface code changes.

import { redirect } from "react-router";
import { fetchJSON, apiUrl } from "../api/client";
import { authUrl } from "./client";
import { isApiError } from "../api/envelope";

export type AppRole = "super_admin" | "user";

export interface AppSession {
  user: {
    id: string;
    email: string;
    roles: AppRole[];
  };
  userPanelEnabled: boolean;
  impersonating: boolean;
}

/**
 * Resolve the current session, or null if unauthenticated.
 *
 * Strategy (capability probe — no /api/v1/me exists):
 *   1. GET /api/v1/admin/sessions:
 *        200 → caller can see all tenants → super_admin
 *        403 → authenticated but not admin → user
 *        401 → not authenticated → null
 *   2. userPanelEnabled is inferred from whether self sign-up is reachable.
 *
 * Identity fields (id/email) are best-effort: the dashboard does not strictly
 * need them, and the probe does not expose them. They are left blank unless a
 * future /api/v1/me is wired in.
 */
export async function loadSession(): Promise<AppSession | null> {
  let roles: AppRole[];
  try {
    // limit=1 keeps the probe cheap.
    await fetchJSON(apiUrl("/admin/sessions?limit=1"));
    roles = ["super_admin"];
  } catch (err) {
    if (isApiError(err)) {
      if (err.isUnauthorized) return null;
      if (err.isForbidden) {
        roles = ["user"];
      } else {
        // Any other error: treat as not-authenticated to be safe.
        return null;
      }
    } else {
      return null;
    }
  }

  const userPanelEnabled = await probeUserPanel();

  return {
    user: { id: "", email: "", roles },
    userPanelEnabled,
    impersonating: false,
  };
}

/**
 * Infer USER_PANEL_ENABLED. When the user panel is on, self sign-up is exposed
 * at /auth/email-password/sign-up; when off it 404s. We probe with OPTIONS so
 * we never create an account. Defaults to true on ambiguity (spec default).
 */
async function probeUserPanel(): Promise<boolean> {
  try {
    const res = await fetch(authUrl("/email-password/sign-up"), {
      method: "OPTIONS",
      credentials: "include",
    });
    if (res.status === 404) return false;
    return true;
  } catch {
    return true;
  }
}

/** Guard for protected loaders: throws a redirect to /login when unauthenticated. */
export function requireSession(s: AppSession | null): AppSession {
  if (!s) {
    throw redirect("/login");
  }
  return s;
}

/** Guard for role-gated loaders: throws a redirect home when the role is missing. */
export function requireRole(s: AppSession, role: AppRole): void {
  if (!s.user.roles.includes(role)) {
    throw redirect("/");
  }
}

export function hasRole(s: AppSession, role: AppRole): boolean {
  return s.user.roles.includes(role);
}
