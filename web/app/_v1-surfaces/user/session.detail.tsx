// User: session detail shell (parent of overview/viewer/contacts). Surface
// agent: user. Renders sub-navigation + an <Outlet/>. The :sessionId param is
// available to all nested surfaces.

import { NavLink, Outlet, useParams } from "react-router";
import { requireUserPanel } from "./_guard";
import { cn } from "~/lib/utils";

export const clientLoader = requireUserPanel;

export default function SessionDetail() {
  const { sessionId = "" } = useParams();
  const base = `/user/sessions/${encodeURIComponent(sessionId)}`;
  const tabs = [
    { to: base, label: "Overview", end: true },
    { to: `${base}/chats`, label: "Chats", end: false },
    { to: `${base}/contacts`, label: "Contacts", end: false },
  ];

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-1 border-b">
        {tabs.map((t) => (
          <NavLink
            key={t.to}
            to={t.to}
            end={t.end}
            className={({ isActive }) =>
              cn(
                "px-3 py-2 text-sm",
                isActive
                  ? "border-b-2 border-primary font-medium"
                  : "text-muted-foreground hover:text-foreground",
              )
            }
          >
            {t.label}
          </NavLink>
        ))}
      </div>
      <Outlet />
    </div>
  );
}
