// Loading skeleton + terminal state screens for the consent page. All copy
// comes from the consent i18n bundle (i18n.tsx).

import * as React from "react";
import {
  CheckCircle2Icon,
  CircleXIcon,
  ClockIcon,
  Loader2Icon,
  ShieldXIcon,
} from "lucide-react";
import { Skeleton } from "~/components/ui/skeleton";
import { cn } from "~/lib/utils";
import { useConsentI18n } from "./i18n";

/** Shown while the fragment is read and the first snapshot is in flight. */
export function LoadingSkeleton() {
  const { m } = useConsentI18n();
  return (
    <div className="flex flex-col gap-6" aria-busy="true" aria-label={m.loading}>
      <div className="flex flex-col items-center gap-3">
        <Skeleton className="size-14 rounded-full" />
        <Skeleton className="h-5 w-40" />
        <Skeleton className="h-4 w-52" />
      </div>
      <Skeleton className="h-28 w-full rounded-lg" />
      <Skeleton className="h-12 w-full rounded-lg" />
      <Skeleton className="mx-auto h-40 w-40 rounded-lg" />
      <Skeleton className="mx-auto h-4 w-32" />
    </div>
  );
}

interface TerminalScreenProps {
  icon: React.ReactNode;
  tone: "success" | "error" | "neutral";
  title: string;
  message: string;
}

function TerminalScreen({ icon, tone, title, message }: TerminalScreenProps) {
  return (
    <div className="flex flex-col items-center gap-4 py-6 text-center">
      <div
        className={cn(
          "flex size-14 items-center justify-center rounded-full ring-8",
          tone === "success" &&
            "bg-emerald-500/10 text-emerald-600 ring-emerald-500/5 dark:text-emerald-500",
          tone === "error" && "bg-destructive/10 text-destructive ring-destructive/5",
          tone === "neutral" && "bg-muted text-muted-foreground ring-muted/40",
        )}
      >
        {icon}
      </div>
      <div className="space-y-1.5">
        <h1 className="text-lg font-semibold tracking-tight">{title}</h1>
        <p className="text-sm text-muted-foreground text-balance">{message}</p>
      </div>
    </div>
  );
}

export function VerifiedScreen() {
  const { m } = useConsentI18n();
  return (
    <TerminalScreen
      tone="success"
      icon={<Loader2Icon className="size-7 animate-spin" aria-hidden />}
      title={m.states.verified.title}
      message={m.states.verified.message}
    />
  );
}

export function FinalizingScreen() {
  const { m } = useConsentI18n();
  return (
    <TerminalScreen
      tone="success"
      icon={<CheckCircle2Icon className="size-7" aria-hidden />}
      title={m.states.finalizing.title}
      message={m.states.finalizing.message}
    />
  );
}

export function DeniedScreen() {
  const { m } = useConsentI18n();
  return (
    <TerminalScreen
      tone="neutral"
      icon={<ShieldXIcon className="size-7" aria-hidden />}
      title={m.states.denied.title}
      message={m.states.denied.message}
    />
  );
}

export function ExpiredScreen() {
  const { m } = useConsentI18n();
  return (
    <TerminalScreen
      tone="neutral"
      icon={<ClockIcon className="size-7" aria-hidden />}
      title={m.states.expired.title}
      message={m.states.expired.message}
    />
  );
}

export function ReloadedScreen() {
  const { m } = useConsentI18n();
  return (
    <TerminalScreen
      tone="neutral"
      icon={<ShieldXIcon className="size-7" aria-hidden />}
      title={m.states.reloaded.title}
      message={m.states.reloaded.message}
    />
  );
}

export function NotFoundScreen() {
  const { m } = useConsentI18n();
  return (
    <TerminalScreen
      tone="error"
      icon={<CircleXIcon className="size-7" aria-hidden />}
      title={m.states.notFound.title}
      message={m.states.notFound.message}
    />
  );
}

export function ErrorScreen() {
  const { m } = useConsentI18n();
  return (
    <TerminalScreen
      tone="error"
      icon={<CircleXIcon className="size-7" aria-hidden />}
      title={m.states.error.title}
      message={m.states.error.message}
    />
  );
}
