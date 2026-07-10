// The consent page's chrome — warm paper backdrop, ambient glows, brand header,
// the elevated card with its WhatsApp-green accent bar, and the footer
// reassurance line. Extracted so the real page (login.whatsapp.tsx) and the
// dashboard's "expand" preview render the exact same shell and cannot drift.

import * as React from "react";
import { LockKeyholeIcon, MessageSquareText } from "lucide-react";
import { Card, CardContent } from "~/components/ui/card";
import { cn } from "~/lib/utils";
import { LanguageToggle, useConsentI18n } from "./i18n";

export function ConsentShell({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  const { m } = useConsentI18n();
  return (
    <div
      className={cn(
        "relative flex w-full flex-col items-center justify-center overflow-hidden bg-[#f4f1e9] p-4 text-[#17231f] dark:bg-[#101915] dark:text-[#eef7f2] sm:p-8",
        className,
      )}
    >
      <div className="pointer-events-none absolute inset-0 opacity-60 [background-image:radial-gradient(circle_at_20%_10%,rgba(37,211,102,.16),transparent_32%),radial-gradient(circle_at_90%_85%,rgba(18,140,126,.13),transparent_36%)]" />

      <div className="relative mb-5 flex w-full max-w-2xl flex-wrap items-center justify-between gap-x-4 gap-y-2 px-1 text-xs font-medium">
        <span className="flex items-center gap-2">
          <MessageSquareText className="size-4 text-emerald-600" aria-hidden />
          WA Gateway
        </span>
        <span className="flex items-center gap-3">
          <span className="flex items-center gap-1.5 text-muted-foreground">
            <LockKeyholeIcon className="size-3.5" aria-hidden />
            {m.secureSignIn}
          </span>
          <LanguageToggle />
        </span>
      </div>

      <Card className="relative w-full max-w-2xl gap-0 overflow-hidden border-black/5 bg-background/95 py-0 shadow-2xl shadow-emerald-950/10 backdrop-blur">
        <div className="h-1 shrink-0 bg-gradient-to-r from-emerald-500 via-[#25d366] to-teal-600" />
        <CardContent className="p-6 sm:p-9">{children}</CardContent>
      </Card>

      <p className="relative mt-5 max-w-md text-center text-xs leading-relaxed text-muted-foreground">
        {m.footerNote}
      </p>
    </div>
  );
}
