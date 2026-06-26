// Placeholder authed surface so the _app layout route has a child and renders
// inside the AppShell. Stage 3 ports the real role-routed surfaces (admin/*,
// user/*, viewer/*, contacts/*) — their v1 sources live in app/_v1-surfaces/.

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
