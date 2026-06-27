// Public AUTH layout route (no app shell). Surface: auth.
//
// A TanStack Start PATHLESS layout route (leading "_" keeps it out of the URL)
// nesting /login, /register and /2fa under one branded shell. Public: no
// session required.
//
// Rebuilt on the shadcn `login-04` block's split-screen layout: a two-column
// full-height grid — the form pane (left) renders the child route's card, the
// brand pane (right) is a self-contained gradient panel (no external image
// asset) shown only at lg+. The three auth forms share this one shell.
//
// beforeLoad gate: if the visitor is ALREADY fully authenticated, skip the auth
// forms entirely and bounce to their role-resolved landing route. The 2fa
// pending state is NOT a full session yet (better-auth holds a short-lived 2FA
// cookie, not a session), so a user mid-2FA is not redirected away.

import { createFileRoute, redirect, Outlet } from "@tanstack/react-router";
import { MessageSquareText, KeyRound, Webhook, Radio } from "lucide-react";
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
    <div className="grid min-h-svh lg:grid-cols-2">
      {/* Form pane */}
      <div className="flex flex-col gap-4 p-6 md:p-10">
        <div className="flex justify-center gap-2 md:justify-start">
          <a href="/" className="flex items-center gap-2 font-medium">
            <div className="flex size-7 items-center justify-center rounded-md bg-primary text-primary-foreground">
              <MessageSquareText className="size-4" aria-hidden />
            </div>
            WA Gateway
          </a>
        </div>
        <div className="flex flex-1 items-center justify-center">
          <div className="w-full max-w-sm">
            <Outlet />
          </div>
        </div>
      </div>

      {/* Brand pane (lg+) */}
      <div className="relative hidden overflow-hidden bg-primary text-primary-foreground lg:flex lg:flex-col lg:justify-between lg:p-12">
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 bg-[radial-gradient(60%_50%_at_70%_20%,rgba(255,255,255,0.12),transparent)]"
        />
        <div className="relative flex items-center gap-2 text-sm font-medium">
          <MessageSquareText className="size-5" aria-hidden />
          WA Gateway
        </div>
        <div className="relative space-y-6">
          <h2 className="text-3xl font-semibold tracking-tight text-balance">
            Realtime WhatsApp infrastructure for your product.
          </h2>
          <ul className="space-y-3 text-sm text-primary-foreground/80">
            <li className="flex items-center gap-3">
              <Radio className="size-4 shrink-0" aria-hidden />
              Pair numbers and stream messages live
            </li>
            <li className="flex items-center gap-3">
              <KeyRound className="size-4 shrink-0" aria-hidden />
              Scoped API keys per organization
            </li>
            <li className="flex items-center gap-3">
              <Webhook className="size-4 shrink-0" aria-hidden />
              Signed webhooks with delivery retries
            </li>
          </ul>
        </div>
        <p className="relative text-xs text-primary-foreground/60">
          Need help getting set up?{" "}
          <a href="/docs" className="underline underline-offset-4">
            Read the docs
          </a>
          .
        </p>
      </div>
    </div>
  );
}
