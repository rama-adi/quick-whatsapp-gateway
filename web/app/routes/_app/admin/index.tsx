// /admin landing → redirect to all-sessions. Ports v1 admin/index.tsx
// (clientLoader throw redirect) to a TanStack Start beforeLoad redirect.

import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_app/admin/")({
  beforeLoad: () => {
    throw redirect({ to: "/admin/sessions" });
  },
});
