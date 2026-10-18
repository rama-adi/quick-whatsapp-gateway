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
  Contact,
  Fingerprint,
  HardDrive,
  House,
  KeyRound,
  Layers,
  LayoutDashboard,
  MessagesSquare,
  QrCode,
  Server,
  Smartphone,
  Users,
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

// Labels are the page names: each page's <PageTitle> and route head() title
// use the same string. Workspace opens with Home (activity + get-started
// checklist), then follows the setup flow (connect a number → get a key →
// receive events → store media → advanced sign-in).
// Every item has a distinct icon so the collapsed icon rail stays readable.
export const NAV: NavItem[] = [
  { to: "/admin/sessions", label: "All sessions", icon: Layers, group: "Admin", show: isAdmin },
  { to: "/admin/gateways", label: "Gateways", icon: Server, group: "Admin", show: isAdmin },
  { to: "/admin/tenants", label: "Users & orgs", icon: Users, group: "Admin", show: isAdmin },
  { to: "/admin/monitor", label: "Event monitor", icon: Activity, group: "Admin", show: isAdmin },
  { to: "/admin/pairing", label: "Number pairing", icon: QrCode, group: "Admin", show: isAdmin },
  { to: "/user/home", label: "Home", icon: House, group: "Workspace", show: isUserPanel },
  { to: "/user/sessions", label: "Sessions", icon: Smartphone, group: "Workspace", show: isUserPanel },
  { to: "/user/keys", label: "API keys", icon: KeyRound, group: "Workspace", show: isUserPanel },
  { to: "/user/webhooks", label: "Webhooks", icon: Webhook, group: "Workspace", show: isUserPanel },
  { to: "/user/storage", label: "Attachment storage", icon: HardDrive, group: "Workspace", show: isUserPanel },
  { to: "/user/oauth-apps", label: "Sign in with WhatsApp", icon: Fingerprint, group: "Workspace", show: isUserPanel },
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

// Section nav for a single WhatsApp session. The session layout
// (user/sessions.$sessionId.tsx) renders these as underline tabs in a row
// directly under the top bar, on every breakpoint. They are deliberately NOT in
// the sidebar: the sidebar keeps one shape on every route, and "Sessions" stays
// its active item while you are inside a session.
export interface SessionNavItem {
  to:
    | "/user/sessions/$sessionId"
    | "/user/sessions/$sessionId/chats"
    | "/user/sessions/$sessionId/contacts";
  label: string;
  icon: LucideIcon;
  /** Match only the exact path (the overview is the parent of the others). */
  exact: boolean;
}

export const SESSION_NAV: readonly SessionNavItem[] = [
  { to: "/user/sessions/$sessionId", label: "Overview", icon: LayoutDashboard, exact: true },
  { to: "/user/sessions/$sessionId/chats", label: "Chats", icon: MessagesSquare, exact: false },
  { to: "/user/sessions/$sessionId/contacts", label: "Contacts", icon: Contact, exact: false },
];
