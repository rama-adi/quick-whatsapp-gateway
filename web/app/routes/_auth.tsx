// Public AUTH layout route (no app shell). Surface: auth.
//
// A TanStack Start PATHLESS layout route (leading "_" keeps it out of the URL)
// nesting /login, /register and /2fa under one branded, centered shell — the
// port of the v1 routes/auth/layout.tsx. Public: no session required.
//
// beforeLoad gate: if the visitor is ALREADY fully authenticated, skip the auth
// forms entirely and bounce to their role-resolved landing route (the v1
// clientLoader did this per-form via loadSession(); here it is hoisted to the
// shared layout so login/register/2fa all inherit it). The 2fa pending state is
// NOT a full session yet (better-auth holds a short-lived 2FA cookie, not a
// session), so a user mid-2FA is not redirected away.

import { createFileRoute, redirect, Outlet } from "@tanstack/react-router";
import { MessageSquareText } from "lucide-react";
import { getServerSession } from "~/lib/auth/session";
import { resolvePostAuthRedirect } from "./_auth/-shared";

export const Route = createFileRoute("/_auth")({
  beforeLoad: async () => {
    const session = await getServerSession();
    if (session) {
      // Runtime-resolved role landing path, not a route literal -> `href`.
      throw redirect({ href: await resolvePostAuthRedirect() });
    }
  },
  component: AuthLayout,
});

function AuthLayout() {
  return (
    <div className="flex min-h-screen flex-col items-center justify-center gap-6 bg-muted/30 p-4">
      <div className="flex flex-col items-center gap-2 text-center">
        <div className="flex size-11 items-center justify-center rounded-xl bg-primary text-primary-foreground">
          <MessageSquareText className="size-6" aria-hidden="true" />
        </div>
        <h1 className="text-lg font-semibold tracking-tight">WA Gateway</h1>
        <p className="text-sm text-muted-foreground">Realtime WhatsApp dashboard</p>
      </div>
      <div className="w-full max-w-sm">
        <Outlet />
      </div>
    </div>
  );
}
