// Public "Sign in with WhatsApp" consent / waiting page — the END-USER face of
// the OIDC provider (docs/specs/oauth.md §6.1).
//
// PUBLIC route: NO beforeLoad, NO better-auth session — end-users are not our
// users. The 160-bit browser code arrives in the URL FRAGMENT (#c=…) so it never
// hits web-server logs or the Referer header; the page reads it client-side and
// opens the public NDJSON wait stream (fetch + ReadableStream, no Bearer). The
// WhatsApp message the user sends IS the consent — there is no Allow/Deny button;
// the branded identity display + Cancel/STOP is the phishing guard (§6.1, §7.3).
//
// Language (EN/ID) is client-side only: localStorage + navigator.language via
// ConsentI18nProvider — nothing about the end-user is stored server-side.

import { createFileRoute } from "@tanstack/react-router";
import { ConsentCard } from "./-oauth/ConsentCard";
import { ConsentShell } from "./-oauth/ConsentShell";
import { ConsentI18nProvider } from "./-oauth/i18n";
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
    <ConsentI18nProvider>
      <main>
        <ConsentShell className="min-h-svh">
          <Body
            phase={phase}
            snapshot={snapshot}
            cancel={cancel}
            cancelling={cancelling}
          />
        </ConsentShell>
      </main>
    </ConsentI18nProvider>
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
