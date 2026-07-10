// The live consent card: app identity, scope list, the verification command +
// deep link / QR, mode instructions, countdown, and the phishing warning.
// Mobile-first — in a narrow container the wa.me button is the hero; the QR is
// the wide-container path (a phone can't scan its own screen). Layout switches
// on CONTAINER queries, not the viewport, so the dashboard's narrow live
// preview faithfully renders the phone experience. oauth.md §6.1.
//
// `preview` renders the same card inert for the dashboard editor: the
// countdown holds still instead of ticking (and resetting on every keystroke)
// and cancel/deep-link do nothing.

import * as React from "react";
import {
  CheckCheckIcon,
  ClockIcon,
  MessageCircleIcon,
  CheckCircle2Icon,
  LockKeyholeIcon,
  UsersIcon,
} from "lucide-react";
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar";
import { Badge } from "~/components/ui/badge";
import { Button } from "~/components/ui/button";
import { cn } from "~/lib/utils";
import { CopyButton } from "./CopyButton";
import { QrCode } from "./QrCode";
import { useConsentI18n } from "./i18n";
import { describeScopes } from "./scopes";
import { formatCountdown, useCountdown } from "./useCountdown";
import { verificationMessage, waMeLink, type PendingSnapshot } from "./protocol";

/** The preview's frozen timer (matches preview.ts's fixed 10-minute expiry). */
const PREVIEW_REMAINING_MS = 600_000;

export function ConsentCard({
  snapshot,
  reconnecting,
  onCancel,
  cancelling,
  preview = false,
}: {
  snapshot: PendingSnapshot;
  reconnecting: boolean;
  onCancel: () => void;
  cancelling: boolean;
  preview?: boolean;
}) {
  const { m, locale } = useConsentI18n();
  const { app, target, user_code, login_command, scopes } = snapshot;
  const command = verificationMessage(login_command, user_code);
  const liveRemaining = useCountdown(
    preview ? null : (snapshot.expires_at ?? null),
  );
  const remaining = preview ? PREVIEW_REMAINING_MS : liveRemaining;
  const scopeLines = describeScopes(scopes, locale);
  const isDm = target.mode === "dm";
  const number = target.number;
  const botName = target.bot_name?.trim() || undefined;
  const groupName = target.mode === "group" ? target.group_name : "";
  const deepLink = number ? waMeLink(number, command) : null;
  const expired = remaining <= 0;

  return (
    <div className="@container">
      <div className="flex flex-col gap-6 @lg:gap-7">
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
            <h1 className="text-lg font-semibold tracking-tight text-balance @lg:text-xl">
              {m.continueTo(app.name)}
            </h1>
            <p className="text-sm text-muted-foreground">
              {isDm ? m.withDm : m.withGroup}
            </p>
          </div>
        </div>

        {/* Scopes */}
        {scopeLines.length > 0 && (
          <div className="rounded-xl border bg-muted/25 p-4 @lg:p-5">
            <p className="mb-3 text-sm font-semibold">
              {m.willReceive(app.name)}
            </p>
            <ul className="space-y-2.5">
              {scopeLines.map((s) => (
                <li key={s.key} className="flex items-start gap-2.5 text-sm">
                  <CheckCircle2Icon
                    className="mt-0.5 size-4 shrink-0 text-emerald-600"
                    aria-hidden
                  />
                  <span>
                    <span className="font-medium">{s.label}</span>
                    <span className="text-muted-foreground"> — {s.description}</span>
                  </span>
                </li>
              ))}
            </ul>
            <div className="mt-4 border-t border-dashed pt-4">
              <div className="flex items-start gap-2.5 text-sm">
                <LockKeyholeIcon
                  className="mt-0.5 size-4 shrink-0 text-emerald-700 dark:text-emerald-400"
                  aria-hidden
                />
                <div>
                  <p className="font-medium">{m.privacyTitle}</p>
                  <p className="mt-0.5 text-muted-foreground">{m.privacyBody}</p>
                </div>
              </div>
            </div>
          </div>
        )}

        {/* The verification instruction */}
        <div className="rounded-2xl border border-emerald-600/15 bg-emerald-500/[0.045] p-4 @lg:p-5">
          <div className="mb-4 flex items-center gap-3">
            <span className="grid size-9 shrink-0 place-items-center rounded-full bg-[#25d366]/15 text-emerald-700 dark:text-emerald-400">
              <MessageCircleIcon className="size-4.5" aria-hidden />
            </span>
            <div>
              <p className="font-semibold leading-tight">{m.confirmTitle}</p>
              <p className="mt-0.5 text-xs text-muted-foreground">
                {m.confirmSubtitle}
              </p>
            </div>
          </div>

          <div className="@lg:grid @lg:grid-cols-[minmax(0,1fr)_auto] @lg:items-start @lg:gap-6">
            <div className="min-w-0 space-y-3">
              <p className="text-sm text-muted-foreground">
                {isDm ? (
                  m.sendTo(
                    <span className="font-medium text-foreground">
                      {formatNumber(number, m.numberFallback)}
                    </span>,
                    botName ?? null,
                  )
                ) : botName ? (
                  m.groupMention(
                    <span className="font-medium text-foreground">
                      {groupName}
                    </span>,
                    <span className="font-medium text-foreground">
                      {botName}
                    </span>,
                  )
                ) : (
                  m.groupMentionFallback(
                    <span className="font-medium text-foreground">
                      {groupName}
                    </span>,
                  )
                )}
              </p>

              {/* WhatsApp-style outgoing bubble: exactly what the sent message
                  looks like. In a group we prepend the styled @mention. */}
              <MessageBubble
                command={command}
                mention={!isDm ? botName : undefined}
              />

              {/* Copy the RAW command only. A WhatsApp @mention can't be pasted
                  — in a group the user types "@", picks the bot, then adds this
                  text. */}
              <div className="flex items-center justify-between gap-3">
                <p className="text-xs text-muted-foreground">
                  {!isDm && botName ? m.copyHintMention : m.copyHintDm}
                </p>
                <CopyButton
                  value={command}
                  label={m.copy}
                  copiedLabel={m.copied}
                  liveMessage={m.copiedLive}
                />
              </div>

              {/* Hero action in a narrow (phone-sized) container: open WhatsApp
                  directly with the message pre-filled. */}
              {isDm && deepLink && (
                <Button
                  asChild
                  size="lg"
                  className="min-h-11 w-full bg-[#128c7e] hover:bg-[#0f766e] @lg:hidden"
                >
                  <a
                    href={preview ? undefined : deepLink}
                    rel="noreferrer"
                    tabIndex={preview ? -1 : undefined}
                    className={cn(preview && "pointer-events-none")}
                  >
                    <MessageCircleIcon aria-hidden />
                    {m.openWhatsApp}
                  </a>
                </Button>
              )}
            </div>

            {/* Wide-container path: QR to open the pre-filled DM on the phone. */}
            {isDm && deepLink && (
              <div className="hidden w-44 flex-col items-center gap-2 @lg:flex">
                <QrCode value={deepLink} size={168} />
                <p className="text-center text-[11px] leading-snug text-muted-foreground">
                  {m.qrCaption}
                </p>
              </div>
            )}
          </div>
        </div>

        {!isDm && (
          <div className="flex items-start gap-2 rounded-lg border bg-muted/30 p-3 text-sm text-muted-foreground">
            <UsersIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
            <span>{m.groupNote}</span>
          </div>
        )}

        {/* Countdown */}
        <div
          className="flex items-center justify-center gap-1.5 text-sm"
          role="status"
          aria-live={expired ? "polite" : "off"}
        >
          <ClockIcon
            className={cn(
              "size-4",
              expired ? "text-destructive" : "text-muted-foreground",
            )}
            aria-hidden
          />
          {expired ? (
            <span className="font-medium text-destructive">{m.codeExpired}</span>
          ) : (
            <span className="text-muted-foreground">
              {m.expiresIn(
                <span className="font-medium tabular-nums text-foreground">
                  {formatCountdown(remaining)}
                </span>,
              )}
            </span>
          )}
          {reconnecting && (
            <Badge variant="outline" className="ml-2 gap-1 text-xs">
              <span className="size-1.5 animate-pulse rounded-full bg-amber-500" />
              {m.reconnecting}
            </Badge>
          )}
        </div>

        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={preview ? undefined : onCancel}
          disabled={cancelling}
          tabIndex={preview ? -1 : undefined}
          className={cn(
            "mx-auto text-muted-foreground",
            preview && "pointer-events-none",
          )}
        >
          {cancelling ? m.cancelling : m.cancelSignIn}
        </Button>
      </div>
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
function formatNumber(number: string | undefined, fallback: string): string {
  if (!number) return fallback;
  const trimmed = number.trim();
  return trimmed.startsWith("+") ? trimmed : `+${trimmed.replace(/^\+*/, "")}`;
}
