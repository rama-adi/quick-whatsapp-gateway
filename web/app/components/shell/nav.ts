// Static, role-filtered navigation model for the app shell.
// FROZEN — owned by the foundation agent. Nav hiding is COSMETIC; the backend
// RBAC + route clientLoaders are the real gate.

import type { AppSession } from "~/lib/auth/session";

export interface NavItem {
  to: string;
  label: string;
  /** Section heading this item groups under. */
  group: "Admin" | "Workspace";
  show: (s: AppSession) => boolean;
}

const isAdmin = (s: AppSession) => s.user.roles.includes("super_admin");
const isUserPanel = (s: AppSession) =>
  s.userPanelEnabled && s.user.roles.includes("user");

export const NAV: NavItem[] = [
  { to: "/admin/tenants", label: "Tenants", group: "Admin", show: isAdmin },
  { to: "/admin/sessions", label: "All Sessions", group: "Admin", show: isAdmin },
  { to: "/admin/monitor", label: "Event Monitor", group: "Admin", show: isAdmin },
  { to: "/admin/pairing", label: "Admin Pairing", group: "Admin", show: isAdmin },
  { to: "/user/sessions", label: "My Sessions", group: "Workspace", show: isUserPanel },
  { to: "/user/keys", label: "API Keys", group: "Workspace", show: isUserPanel },
  { to: "/user/webhooks", label: "Webhooks", group: "Workspace", show: isUserPanel },
];

export function visibleNav(s: AppSession): NavItem[] {
  return NAV.filter((item) => item.show(s));
}
