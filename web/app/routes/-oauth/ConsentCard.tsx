// The live consent card: app identity, scope list, the verification command +
// deep link / QR, mode instructions, countdown, and the phishing warning.
// Mobile-first — on a phone the wa.me button is the hero; the QR is the desktop
// path (a phone can't scan its own screen). oauth.md §6.1.

import * as React from "react";
import {
  ClockIcon,
  ExternalLinkIcon,
  MessageCircleIcon,
  ShieldAlertIcon,
  UsersIcon,
} from "lucide-react";
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar";
import { Badge } from "~/components/ui/badge";
import { Button } from "~/components/ui/button";
import { Separator } from "~/components/ui/separator";
import { cn } from "~/lib/utils";
import { CopyButton } from "./CopyButton";
import { QrCode } from "./QrCode";
import { describeScopes } from "./scopes";
import { formatCountdown, useCountdown } from "./useCountdown";
import { verificationMessage, waMeLink, type PendingSnapshot } from "./protocol";

export function ConsentCard({
  snapshot,
  reconnecting,
  onCancel,
  cancelling,
}: {
  snapshot: PendingSnapshot;
  reconnecting: boolean;
  onCancel: () => void;
  cancelling: boolean;
}) {
  const { app, target, user_code, login_command, scopes } = snapshot;
  const command = verificationMessage(login_command, user_code);
  const remaining = useCountdown(snapshot.expires_at ?? null);
  const scopeLines = describeScopes(scopes);
  const isDm = target.mode === "dm";
  const number = target.mode === "dm" ? target.number : target.number;
  const deepLink = number ? waMeLink(number, command) : null;
  const expired = remaining <= 0;

  return (
    <div className="flex flex-col gap-6">
      {/* App identity */}
      <div className="flex flex-col items-center gap-3 text-center">
        <Avatar size="lg" className="size-14 ring-1 ring-border">
          {app.logo ? <AvatarImage src={app.logo} alt="" /> : null}
          <AvatarFallback className="text-base font-semibold">
            {initials(app.name)}
          </AvatarFallback>
        </Avatar>
        <div className="space-y-1">
          <h1 className="text-lg font-semibold tracking-tight text-balance">
            Sign in to {app.name}
          </h1>
          <p className="text-sm text-muted-foreground">
            with your WhatsApp {isDm ? "number" : "group membership"}
          </p>
        </div>
      </div>

      {/* Scopes */}
      {scopeLines.length > 0 && (
        <div className="rounded-lg border bg-muted/30 p-4">
          <p className="mb-3 text-xs font-medium text-muted-foreground uppercase tracking-wide">
            {app.name} will learn
          </p>
          <ul className="space-y-2.5">
            {scopeLines.map((s) => (
              <li key={s.key} className="flex items-start gap-2.5 text-sm">
                <span className="mt-1.5 size-1.5 shrink-0 rounded-full bg-primary/70" />
                <span>
                  <span className="font-medium">{s.label}</span>
                  <span className="text-muted-foreground"> — {s.description}</span>
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}

      {/* The verification instruction */}
      <div className="space-y-3">
        <p className="text-sm text-muted-foreground">
          {isDm ? (
            <>
              Send this message to{" "}
              <span className="font-medium text-foreground">
                {formatNumber(number)}
              </span>{" "}
              on WhatsApp:
            </>
          ) : (
            <>
              In the group{" "}
              <span className="font-medium text-foreground">
                {target.mode === "group" ? target.group_name : ""}
              </span>
              , mention the bot with this message:
            </>
          )}
        </p>

        <div className="flex items-center justify-between gap-2 rounded-lg border bg-card px-3 py-3">
          <code className="font-mono text-base font-semibold tracking-wide sm:text-lg">
            {command}
          </code>
          <CopyButton value={command} />
        </div>

        {/* Hero action: on mobile, open WhatsApp directly. */}
        {isDm && deepLink && (
          <Button asChild size="lg" className="w-full sm:hidden">
            <a href={deepLink} rel="noreferrer">
              <MessageCircleIcon aria-hidden />
              Open WhatsApp
            </a>
          </Button>
        )}
      </div>

      {/* Desktop path: QR to open the pre-filled DM on the phone. */}
      {isDm && deepLink && (
        <div className="hidden flex-col items-center gap-2 sm:flex">
          <QrCode value={deepLink} size={176} />
          <p className="text-xs text-muted-foreground">
            Scan to open the message on your phone
          </p>
        </div>
      )}

      {!isDm && (
        <div className="flex items-start gap-2 rounded-lg border bg-muted/30 p-3 text-sm text-muted-foreground">
          <UsersIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
          <span>
            You must already be a member of the group. Sending this from inside
            the group proves your membership.
          </span>
        </div>
      )}

      {/* Countdown */}
      <div className="flex items-center justify-center gap-1.5 text-sm">
        <ClockIcon
          className={cn("size-4", expired ? "text-destructive" : "text-muted-foreground")}
          aria-hidden
        />
        {expired ? (
          <span className="font-medium text-destructive">Code expired</span>
        ) : (
          <span className="text-muted-foreground">
            Expires in{" "}
            <span className="font-medium tabular-nums text-foreground">
              {formatCountdown(remaining)}
            </span>
          </span>
        )}
        {reconnecting && (
          <Badge variant="outline" className="ml-2 gap-1 text-xs">
            <span className="size-1.5 animate-pulse rounded-full bg-amber-500" />
            Reconnecting
          </Badge>
        )}
      </div>

      <Separator />

      {/* Phishing guard */}
      <div className="flex items-start gap-2.5 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3 text-sm">
        <ShieldAlertIcon className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-500" aria-hidden />
        <span className="text-muted-foreground">
          <span className="font-medium text-foreground">
            Only continue if you started this sign-in yourself.
          </span>{" "}
          {app.name} will never ask you to send this code somewhere else.
        </span>
      </div>

      <Button
        type="button"
        variant="ghost"
        size="sm"
        onClick={onCancel}
        disabled={cancelling}
        className="mx-auto text-muted-foreground"
      >
        {cancelling ? "Cancelling…" : "This isn't me / Cancel"}
      </Button>
    </div>
  );
}

function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0]!.slice(0, 2).toUpperCase();
  return (parts[0]![0]! + parts[parts.length - 1]![0]!).toUpperCase();
}

/** Light formatting for a raw phone number without mangling it. */
function formatNumber(number?: string): string {
  if (!number) return "the app's number";
  const trimmed = number.trim();
  return trimmed.startsWith("+") ? trimmed : `+${trimmed.replace(/^\+*/, "")}`;
}
