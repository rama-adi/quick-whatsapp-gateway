// Placeholder authed surface so the _app layout route has a child and renders
// inside the AppShell. The real role-routed surfaces live under user/* (and the
// other _app children); this index stays a minimal landing page.

import { createFileRoute } from "@tanstack/react-router";

export const Route = createFileRoute("/_app/dashboard")({
  component: Dashboard,
});

function Dashboard() {
  return (
    <div className="space-y-2">
      <h1 className="text-xl font-semibold">Dashboard</h1>
      <p className="text-sm text-muted-foreground">
        Authed shell placeholder. Real surfaces land in a later stage.
      </p>
    </div>
  );
}
