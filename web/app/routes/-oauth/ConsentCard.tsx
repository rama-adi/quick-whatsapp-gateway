// The live consent card: app identity, scope list, the verification command +
// deep link / QR, mode instructions, countdown, and the phishing warning.
// Mobile-first — on a phone the wa.me button is the hero; the QR is the desktop
// path (a phone can't scan its own screen). oauth.md §6.1.

import * as React from "react";
import {
  CheckCheckIcon,
  ClockIcon,
  MessageCircleIcon,
  CheckCircle2Icon,
  ShieldAlertIcon,
  UsersIcon,
} from "lucide-react";
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar";
import { Badge } from "~/components/ui/badge";
import { Button } from "~/components/ui/button";
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
  const number = target.number;
  const botName = target.bot_name?.trim() || undefined;
  const groupName = target.mode === "group" ? target.group_name : "";
  const deepLink = number ? waMeLink(number, command) : null;
  const expired = remaining <= 0;

  return (
    <div className="flex flex-col gap-7">
      {/* App identity */}
      <div className="flex flex-col items-center gap-4 text-center">
        <div className="relative">
          <Avatar size="lg" className="size-16 ring-4 ring-emerald-500/10">
            {app.logo ? (
              <AvatarImage src={app.logo} alt={`${app.name} logo`} />
            ) : null}
            <AvatarFallback className="text-base font-semibold">
              {initials(app.name)}
            </AvatarFallback>
          </Avatar>
          <span className="absolute -bottom-1 -right-1 grid size-6 place-items-center rounded-full border-2 border-background bg-[#25d366] text-white">
            <CheckCheckIcon className="size-3.5" aria-hidden />
          </span>
        </div>
        <div className="space-y-1">
          <h1 className="text-lg font-semibold tracking-tight text-balance">
            Continue to {app.name}
          </h1>
          <p className="text-sm text-muted-foreground">
            with your WhatsApp {isDm ? "number" : "group membership"}
          </p>
        </div>
      </div>

      {/* Scopes */}
      {scopeLines.length > 0 && (
        <div className="rounded-xl border bg-muted/25 p-4 sm:p-5">
          <p className="mb-3 text-sm font-semibold">
            What {app.name} will receive
          </p>
          <ul className="space-y-2.5">
            {scopeLines.map((s) => (
              <li key={s.key} className="flex items-start gap-2.5 text-sm">
                <CheckCircle2Icon className="mt-0.5 size-4 shrink-0 text-emerald-600" aria-hidden />
                <span>
                  <span className="font-medium">{s.label}</span>
                  <span className="text-muted-foreground"> — {s.description}</span>
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}

      {/* Phishing guard */}
      <div className="flex items-start gap-2.5 rounded-xl border bg-muted/30 p-3.5 text-sm">
        <ShieldAlertIcon
          className="mt-0.5 size-4 shrink-0 text-emerald-700 dark:text-emerald-400"
          aria-hidden
        />
        <span className="text-muted-foreground">
          <span className="font-medium text-foreground">
            Only continue if you started this sign-in yourself.
          </span>{" "}
          {app.name} will never ask you to send this code somewhere else.
        </span>
      </div>

      {/* The verification instruction */}
      <div className="rounded-2xl border border-emerald-600/15 bg-emerald-500/[0.04] p-4 sm:p-5">
        <div className="mb-4">
          <div>
            <p className="font-semibold">Confirm in WhatsApp</p>
            <p className="text-xs text-muted-foreground">
              Send one message to prove it is you.
            </p>
          </div>
        </div>
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
          {isDm ? (
            <>
              Send this message to{" "}
              <span className="font-medium text-foreground">
                {formatNumber(number)}
              </span>
              {botName ? (
                <span className="text-muted-foreground"> ({botName})</span>
              ) : null}{" "}
              on WhatsApp:
            </>
          ) : botName ? (
            <>
              In{" "}
              <span className="font-medium text-foreground">{groupName}</span>,
              type <code className="font-mono">@</code> and pick{" "}
              <span className="font-medium text-foreground">{botName}</span> from
              the suggestions, then send:
            </>
          ) : (
            <>
              In the group{" "}
              <span className="font-medium text-foreground">{groupName}</span>,
              mention the bot with this message:
            </>
          )}
          </p>

        {/* WhatsApp-style outgoing bubble: exactly what the sent message looks
            like. In a group we prepend the styled @mention of the bot. */}
        <MessageBubble
          command={command}
          mention={!isDm ? botName : undefined}
        />

        {/* Copy the RAW command only. A WhatsApp @mention can't be pasted — in a
            group the user types "@", picks the bot, then adds this text. */}
        <div className="flex items-center justify-between gap-3">
          <p className="text-xs text-muted-foreground">
            {!isDm && botName
              ? "The @mention can't be copied — pick the bot in WhatsApp, then add:"
              : "Copy and send it exactly as shown."}
          </p>
          <CopyButton value={command} />
        </div>

        {/* Hero action: on mobile, open WhatsApp directly. */}
        {isDm && deepLink && (
          <Button asChild size="lg" className="min-h-11 w-full bg-[#128c7e] hover:bg-[#0f766e] sm:hidden">
            <a href={deepLink} rel="noreferrer">
              <MessageCircleIcon aria-hidden />
              Open WhatsApp
            </a>
          </Button>
        )}
        </div>
      </div>

      {/* Desktop path: QR to open the pre-filled DM on the phone. */}
      {isDm && deepLink && (
        <div className="hidden flex-col items-center gap-2 sm:flex">
          <QrCode value={deepLink} size={176} />
          <p className="text-xs text-muted-foreground">
            Scan with your phone to open the prepared message
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
      <div
        className="flex items-center justify-center gap-1.5 text-sm"
        role="status"
        aria-live={expired ? "polite" : "off"}
      >
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

      <Button
        type="button"
        variant="ghost"
        size="sm"
        onClick={onCancel}
        disabled={cancelling}
        className="mx-auto text-muted-foreground"
      >
        {cancelling ? "Cancelling…" : "Cancel sign-in"}
      </Button>
    </div>
  );
}

/**
 * A single WhatsApp-style outgoing message bubble (WhatsApp dark-mode green),
 * showing verbatim the text the end-user must send. When `mention` is set, the
 * leading "@name" is rendered in the WhatsApp mention teal; the rest is the raw
 * command. The bubble carries its own dark background + white text, so it reads
 * the same in both the site's light and dark themes.
 */
function MessageBubble({
  command,
  mention,
}: {
  command: string;
  mention?: string;
}) {
  return (
    <div className="flex justify-end">
      <div className="max-w-[90%] rounded-2xl rounded-br-md bg-[#005c4b] px-3 py-2 shadow-sm">
        <p className="text-sm leading-snug break-words text-white">
          {mention ? (
            <span className="font-semibold text-[#53bdeb]">@{mention} </span>
          ) : null}
          {command}
        </p>
      </div>
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
