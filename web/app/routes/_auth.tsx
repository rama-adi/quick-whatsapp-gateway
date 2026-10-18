// Public AUTH layout route (no app shell). Surface: auth.
//
// A TanStack Start PATHLESS layout route (leading "_" keeps it out of the URL)
// nesting /login, /register and /2fa under one branded shell. Public: no
// session required.
//
// Rebuilt on the shadcn `login-04` block's split-screen layout: a two-column
// full-height grid — the form pane (left) renders the child route's card, the
// brand pane (right) is a self-contained gradient panel (no external image
// asset) shown only at lg+, inset from the window edge as a rounded panel to
// match the app shell's frame + panel language. The three auth forms share
// this one shell.
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
    <div className="grid min-h-svh bg-background lg:grid-cols-2">
      {/* Form pane */}
      <div className="flex flex-col gap-4 p-6 md:p-10">
        <div className="flex justify-center gap-2 md:justify-start">
          <a
            href="/"
            className="flex items-center gap-2.5 text-sm font-semibold tracking-tight"
          >
            <div className="flex size-7 items-center justify-center rounded-lg bg-primary text-primary-foreground shadow-xs">
              <MessageSquareText className="size-4" aria-hidden />
            </div>
            WA Gateway
          </a>
        </div>
        <div className="flex flex-1 items-center justify-center">
          <div className="w-full max-w-sm animate-in duration-300 ease-soft fade-in-0 slide-in-from-bottom-1">
            <Outlet />
          </div>
        </div>
      </div>

      {/* Brand pane (lg+): an inset rounded panel on the page, like the app
          shell's content panel. Fixed near-black (the light-mode primary) so
          it stays a dark pane in dark mode instead of inverting to white. */}
      <div className="relative m-2 hidden overflow-hidden rounded-panel bg-neutral-900 text-neutral-50 shadow-panel ring-1 ring-foreground/5 lg:flex lg:flex-col lg:justify-between lg:p-12 dark:bg-card dark:ring-white/10">
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 bg-[radial-gradient(60%_50%_at_70%_20%,rgba(255,255,255,0.12),transparent),radial-gradient(40%_40%_at_10%_100%,rgba(255,255,255,0.06),transparent)]"
        />
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 [mask-image:radial-gradient(70%_60%_at_70%_30%,black,transparent)] bg-[linear-gradient(to_right,rgba(255,255,255,0.05)_1px,transparent_1px),linear-gradient(to_bottom,rgba(255,255,255,0.05)_1px,transparent_1px)] bg-[size:32px_32px]"
        />
        <div className="relative flex items-center gap-2 text-sm font-medium">
          <MessageSquareText className="size-5" aria-hidden />
          WA Gateway
        </div>
        <div className="relative space-y-6">
          <h2 className="max-w-md text-3xl font-semibold tracking-tight text-balance">
            Realtime WhatsApp infrastructure for your product.
          </h2>
          <ul className="space-y-3 text-sm text-neutral-50/80">
            <li className="flex items-center gap-3">
              <FeatureIcon>
                <Radio className="size-3.5" aria-hidden />
              </FeatureIcon>
              Pair numbers and stream messages live
            </li>
            <li className="flex items-center gap-3">
              <FeatureIcon>
                <KeyRound className="size-3.5" aria-hidden />
              </FeatureIcon>
              Scoped API keys per organization
            </li>
            <li className="flex items-center gap-3">
              <FeatureIcon>
                <Webhook className="size-3.5" aria-hidden />
              </FeatureIcon>
              Signed webhooks with delivery retries
            </li>
          </ul>
        </div>
        <p className="relative text-xs text-neutral-50/60">
          Need help getting set up?{" "}
          <a href="/docs" className="underline underline-offset-4 hover:text-neutral-50">
            Read the docs
          </a>
          .
        </p>
      </div>
    </div>
  );
}

function FeatureIcon({ children }: { children: React.ReactNode }) {
  return (
    <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-white/10 ring-1 ring-white/15">
      {children}
    </span>
  );
}
