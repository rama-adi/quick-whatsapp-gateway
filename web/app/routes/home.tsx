// Role-routed landing: send admins to the admin surface, users to their
// sessions. Lives inside the authenticated shell, so the session context is set.

import { redirect } from "react-router";
import { loadSession } from "~/lib/auth/session";
import { Placeholder } from "~/components/shell/Placeholder";

export async function clientLoader() {
  const session = await loadSession();
  if (!session) throw redirect("/login");
  if (session.user.roles.includes("super_admin")) {
    throw redirect("/admin/sessions");
  }
  if (session.userPanelEnabled && session.user.roles.includes("user")) {
    throw redirect("/user/sessions");
  }
  return null;
}

export default function Home() {
  return (
    <Placeholder
      title="Welcome"
      description="Your account has no enabled surfaces. Contact an administrator."
    />
  );
}
