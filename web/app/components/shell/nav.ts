// Static, role-filtered navigation model for the app shell.
// Owned by the foundation agent. Nav hiding is COSMETIC; the backend RBAC +
// route clientLoaders are the real gate.
//
// Consumed by AppShell -> AppSidebar (shadcn sidebar). Each item carries a
// lucide icon so the sidebar can render an icon + label and collapse to
// icon-only. The order here is the display order; the first item of each
// group is also that group's natural landing target.

import type { LucideIcon } from "lucide-react";
import {
  Activity,
  BookOpen,
  Building2,
  Fingerprint,
  KeyRound,
  QrCode,
  ServerCog,
  Smartphone,
  Webhook,
} from "lucide-react";
import type { AppSession } from "~/lib/auth/session";

export interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  /** Section heading this item groups under. */
  group: "Admin" | "Workspace";
  show: (s: AppSession) => boolean;
}

const isAdmin = (s: AppSession) => s.user.roles.includes("super_admin");
const isUserPanel = (s: AppSession) =>
  s.userPanelEnabled && s.user.roles.includes("user");

export const NAV: NavItem[] = [
  { to: "/admin/sessions", label: "All Sessions", icon: ServerCog, group: "Admin", show: isAdmin },
  { to: "/admin/tenants", label: "Tenants", icon: Building2, group: "Admin", show: isAdmin },
  { to: "/admin/monitor", label: "Event Monitor", icon: Activity, group: "Admin", show: isAdmin },
  { to: "/admin/pairing", label: "Pairing", icon: QrCode, group: "Admin", show: isAdmin },
  { to: "/user/sessions", label: "My Sessions", icon: Smartphone, group: "Workspace", show: isUserPanel },
  { to: "/user/keys", label: "API Keys", icon: KeyRound, group: "Workspace", show: isUserPanel },
  { to: "/user/oauth-apps", label: "Sign in with WhatsApp", icon: Fingerprint, group: "Workspace", show: isUserPanel },
  { to: "/user/webhooks", label: "Webhooks", icon: Webhook, group: "Workspace", show: isUserPanel },
];

export function visibleNav(s: AppSession): NavItem[] {
  return NAV.filter((item) => item.show(s));
}

// Always-visible help entry point (the docs site is public and shares the app
// theme). Kept out of NAV because it is not role-gated and renders in the
// sidebar footer rather than a Workspace/Admin group.
export const DOCS_NAV = {
  href: "/docs",
  label: "Documentation",
  icon: BookOpen,
} as const;
