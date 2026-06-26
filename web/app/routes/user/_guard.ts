// Shared guard for user-panel routes. Surface agent: user may extend.
import { redirect } from "react-router";
import { loadSession, requireSession } from "~/lib/auth/session";

export async function requireUserPanel(): Promise<null> {
  const session = requireSession(await loadSession());
  if (!session.userPanelEnabled || !session.user.roles.includes("user")) {
    throw redirect("/");
  }
  return null;
}
