// Public "Sign in with WhatsApp" consent / waiting page — the END-USER face of
// the OIDC provider (docs/specs/oauth.md §6.1).
//
// PUBLIC route: NO beforeLoad, NO better-auth session — end-users are not our
// users. The 160-bit browser code arrives in the URL FRAGMENT (#c=…) so it never
// hits web-server logs or the Referer header; the page reads it client-side and
// opens the public NDJSON wait stream (fetch + ReadableStream, no Bearer). The
// WhatsApp message the user sends IS the consent — there is no Allow/Deny button;
// the branded identity display + Cancel/STOP is the phishing guard (§6.1, §7.3).

import { createFileRoute } from "@tanstack/react-router";
import { LockKeyholeIcon, MessageSquareText } from "lucide-react";
import { Card, CardContent } from "~/components/ui/card";
import { ConsentCard } from "./-oauth/ConsentCard";
import { useWait } from "./-oauth/useWait";
import {
  DeniedScreen,
  ErrorScreen,
  ExpiredScreen,
  FinalizingScreen,
  LoadingSkeleton,
  NotFoundScreen,
  ReloadedScreen,
  VerifiedScreen,
} from "./-oauth/states";

export const Route = createFileRoute("/login/whatsapp")({
  // Sensitive capability page: keep it out of caches and referrers (§6.1). SSR
  // is irrelevant here (the code lives in the fragment, invisible to the server)
  // so we render purely client-side.
  head: () => ({
    meta: [
      { title: "Sign in with WhatsApp" },
      { name: "robots", content: "noindex, nofollow" },
      { name: "referrer", content: "no-referrer" },
    ],
  }),
  component: WhatsAppLoginPage,
});

function WhatsAppLoginPage() {
  const { phase, snapshot, cancel, cancelling } = useWait();

  return (
    <main className="relative flex min-h-svh flex-col items-center justify-center overflow-hidden bg-[#f4f1e9] p-4 text-[#17231f] dark:bg-[#101915] dark:text-[#eef7f2] sm:p-8">
      <div className="pointer-events-none absolute inset-0 opacity-60 [background-image:radial-gradient(circle_at_20%_10%,rgba(37,211,102,.16),transparent_32%),radial-gradient(circle_at_90%_85%,rgba(18,140,126,.13),transparent_36%)]" />
      <div className="relative mb-5 flex w-full max-w-2xl items-center justify-between px-1 text-xs font-medium">
        <span className="flex items-center gap-2"><MessageSquareText className="size-4 text-emerald-600" aria-hidden /> WA Gateway</span>
        <span className="flex items-center gap-1.5 text-muted-foreground"><LockKeyholeIcon className="size-3.5" aria-hidden /> Secure sign-in</span>
      </div>
      <Card className="relative w-full max-w-2xl overflow-hidden border-black/5 bg-background/95 shadow-2xl shadow-emerald-950/10 backdrop-blur">
        <div className="h-1 bg-gradient-to-r from-emerald-500 via-[#25d366] to-teal-600" />
        <CardContent className="p-6 sm:p-9">
          <Body
            phase={phase}
            snapshot={snapshot}
            cancel={cancel}
            cancelling={cancelling}
          />
        </CardContent>
      </Card>

      <p className="relative mt-5 max-w-md text-center text-xs leading-relaxed text-muted-foreground">Your sign-in code is short-lived and is only confirmed after you send it from WhatsApp.</p>
    </main>
  );
}

function Body({
  phase,
  snapshot,
  cancel,
  cancelling,
}: ReturnType<typeof useWait>) {
  switch (phase) {
    case "loading":
      return <LoadingSkeleton />;
    case "pending":
    case "reconnecting":
      // Snapshot must exist in these phases; guard for the type-narrowing.
      return snapshot ? (
        <ConsentCard
          snapshot={snapshot}
          reconnecting={phase === "reconnecting"}
          onCancel={cancel}
          cancelling={cancelling}
        />
      ) : (
        <LoadingSkeleton />
      );
    case "finalizing":
      return <FinalizingScreen />;
    case "verified":
      return <VerifiedScreen />;
    case "denied":
      return <DeniedScreen />;
    case "expired":
      return <ExpiredScreen />;
    case "reloaded":
      return <ReloadedScreen />;
    case "not_found":
      return <NotFoundScreen />;
    case "error":
      return <ErrorScreen />;
  }
}
