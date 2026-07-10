// The live consent preview beside the OAuth app form (oauth.md §6.2): a
// phone-width inline card plus an "expand" dialog that renders the real
// end-user page shell (ConsentShell) at full size. Mounts its own
// non-persisting i18n provider so a developer can flip EN/ID to proofread both
// languages without changing their own end-user default.

import * as React from "react";
import { Maximize2Icon } from "lucide-react";
import { Button } from "~/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "~/components/ui/dialog";
import { ConsentCard } from "~/routes/-oauth/ConsentCard";
import { ConsentShell } from "~/routes/-oauth/ConsentShell";
import { ConsentI18nProvider, LanguageToggle } from "~/routes/-oauth/i18n";
import type { PendingSnapshot } from "~/routes/-oauth/protocol";

export function ConsentPreview({ snapshot }: { snapshot: PendingSnapshot }) {
  return (
    <ConsentI18nProvider persist={false}>
      <div className="mb-3 flex items-center justify-between gap-3">
        <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          Sign-in preview
        </p>
        <div className="flex items-center gap-2">
          <LanguageToggle />
          <ExpandDialog snapshot={snapshot} />
        </div>
      </div>

      {/* Inline: phone width. The card lays itself out with container queries,
          so this narrow column shows exactly the mobile experience. */}
      <div className="overflow-hidden rounded-2xl border bg-[#f4f1e9] shadow-sm dark:bg-[#101915]">
        <div className="p-4">
          <div className="overflow-hidden rounded-xl border border-black/5 bg-background shadow-xl shadow-emerald-950/10">
            <div className="h-1 bg-gradient-to-r from-emerald-500 via-[#25d366] to-teal-600" />
            <div className="p-5">
              <PreviewCard snapshot={snapshot} />
            </div>
          </div>
        </div>
      </div>
      <p className="mt-2 text-center text-xs text-muted-foreground">
        Exactly what end-users see, in their language. It updates as you edit.
      </p>
    </ConsentI18nProvider>
  );
}

/** The full-page preview: a pinned header (title + language), and the real
 * page shell scrolling beneath it. */
function ExpandDialog({ snapshot }: { snapshot: PendingSnapshot }) {
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="ghost" size="sm" className="h-8 gap-1.5">
          <Maximize2Icon className="size-3.5" aria-hidden />
          Expand
        </Button>
      </DialogTrigger>
      <DialogContent className="flex max-h-[calc(100svh-2.5rem)] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
        {/* The language toggle lives inside the previewed page itself (the
            shell header), same as end-users get it. */}
        <DialogHeader className="shrink-0 gap-1 border-b px-5 py-4 pr-12 text-left sm:px-6">
          <DialogTitle>Full sign-in preview</DialogTitle>
          <DialogDescription>
            The consent page your users will see, rendered at full size.
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 overflow-y-auto">
          <ConsentShell className="min-h-full">
            <PreviewCard snapshot={snapshot} />
          </ConsentShell>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function PreviewCard({ snapshot }: { snapshot: PendingSnapshot }) {
  return (
    <ConsentCard
      snapshot={snapshot}
      reconnecting={false}
      onCancel={() => {}}
      cancelling={false}
      preview
    />
  );
}
