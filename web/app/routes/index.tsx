// Home / landing. Stage 1 placeholder. In a later stage this becomes the
// role-routed entry (super_admin -> /admin, user -> /user) once the real auth
// session + surfaces are wired.

import { createFileRoute } from "@tanstack/react-router";

export const Route = createFileRoute("/")({
  component: Home,
});

function Home() {
  return (
    <main className="container mx-auto flex min-h-screen flex-col items-center justify-center gap-4 p-8 text-center">
      <h1 className="text-3xl font-semibold">WA Gateway</h1>
      <p className="text-muted-foreground">
        TanStack Start skeleton is up. Auth and the role-routed dashboard land in
        the next stage.
      </p>
    </main>
  );
}
