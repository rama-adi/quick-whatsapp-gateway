// Loading skeleton + terminal state screens for the consent page.

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

/** Shown while the fragment is read and the first snapshot is in flight. */
export function LoadingSkeleton() {
  return (
    <div className="flex flex-col gap-6" aria-busy="true" aria-label="Loading">
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
          "flex size-14 items-center justify-center rounded-full",
          tone === "success" && "bg-emerald-500/10 text-emerald-600 dark:text-emerald-500",
          tone === "error" && "bg-destructive/10 text-destructive",
          tone === "neutral" && "bg-muted text-muted-foreground",
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
  return (
    <TerminalScreen
      tone="success"
      icon={<Loader2Icon className="size-7 animate-spin" aria-hidden />}
      title="Signed in"
      message="Taking you back to the app…"
    />
  );
}

export function FinalizingScreen() {
  return (
    <TerminalScreen
      tone="success"
      icon={<CheckCircle2Icon className="size-7" aria-hidden />}
      title="Verified"
      message="Finishing sign-in…"
    />
  );
}

export function DeniedScreen() {
  return (
    <TerminalScreen
      tone="neutral"
      icon={<ShieldXIcon className="size-7" aria-hidden />}
      title="Sign-in cancelled"
      message="This sign-in was cancelled. You can safely close this tab. If you were trying to sign in, start again from the app."
    />
  );
}

export function ExpiredScreen() {
  return (
    <TerminalScreen
      tone="neutral"
      icon={<ClockIcon className="size-7" aria-hidden />}
      title="Sign-in expired"
      message="This code timed out. Return to the app and start signing in again to get a new one."
    />
  );
}

export function ReloadedScreen() {
  return (
    <TerminalScreen
      tone="neutral"
      icon={<ShieldXIcon className="size-7" aria-hidden />}
      title="Sign-in cancelled"
      message="This page was reloaded, so the sign-in attempt was cancelled for your security. Return to the app and start again."
    />
  );
}

export function NotFoundScreen() {
  return (
    <TerminalScreen
      tone="error"
      icon={<CircleXIcon className="size-7" aria-hidden />}
      title="Invalid or expired link"
      message="This sign-in link is no longer valid. Go back to the app and start again."
    />
  );
}

export function ErrorScreen() {
  return (
    <TerminalScreen
      tone="error"
      icon={<CircleXIcon className="size-7" aria-hidden />}
      title="Something went wrong"
      message="We couldn't finish signing you in. Return to the app and try again."
    />
  );
}
