// Shared super_admin guard for admin routes. Surface agent: admin may extend.
import { loadSession, requireSession, requireRole } from "~/lib/auth/session";

export async function requireAdmin(): Promise<null> {
  const session = requireSession(await loadSession());
  requireRole(session, "super_admin");
  return null;
}
