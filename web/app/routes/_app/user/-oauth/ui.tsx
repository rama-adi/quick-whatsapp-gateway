// Shared presentational bits for the "Sign in with WhatsApp" (OAuth apps)
// surface: status / mode / client-type badges, the app avatar, a label+value
// row for credentials, and the copy-once client-secret modal (the shared
// SecretRevealDialog, so Esc / outside clicks can't lose the secret —
// oauth.md §6.2 "shown exactly once").

import type { ReactNode } from "react";
import { ShieldCheckIcon } from "lucide-react";
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar";
import { Badge } from "~/components/ui/badge";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "~/components/ui/tooltip";
import { CopyableId } from "~/components/copyable";
import { SecretRevealDialog } from "~/components/secret-reveal";
import { cn } from "~/lib/utils";
import type { OAuthApp, OAuthMode } from "~/lib/api/hooks/oauth";

/** Human names + one-line meaning for each verification mode (and its acr). */
export const MODE_META: Record<OAuthMode, { label: string; description: string }> = {
  dm: {
    label: "Direct message",
    description:
      "The user sends the login code to your number in a private chat. Proves they control the WhatsApp number.",
  },
  group: {
    label: "Group membership",
    description:
      "The user posts the login code in a chosen group. Also proves they are a member of that group.",
  },
};

export function AppStatusBadge({ status }: { status: OAuthApp["status"] }) {
  const active = status === "active";
  return (
    <Badge
      variant="secondary"
      className={cn(
        "gap-1.5",
        active
          ? "bg-emerald-500/12 text-emerald-700 dark:text-emerald-400"
          : "bg-muted text-muted-foreground",
      )}
    >
      <span
        aria-hidden
        className={cn(
          "size-1.5 rounded-full",
          active ? "bg-emerald-500" : "bg-muted-foreground/60",
        )}
      />
      {active ? "Active" : "Disabled"}
    </Badge>
  );
}

export function ModeChips({ modes }: { modes: OAuthMode[] | null | undefined }) {
  const list = modes ?? [];
  if (list.length === 0) {
    return <span className="text-xs text-muted-foreground">—</span>;
  }
  return (
    <TooltipProvider delayDuration={300}>
      <div className="flex flex-wrap gap-1">
        {list.map((m) => (
          <Tooltip key={m}>
            <TooltipTrigger asChild>
              <Badge variant="outline" className="cursor-default">
                {MODE_META[m]?.label ?? m}
              </Badge>
            </TooltipTrigger>
            <TooltipContent className="max-w-64 text-pretty">
              {MODE_META[m]?.description ?? m}
            </TooltipContent>
          </Tooltip>
        ))}
      </div>
    </TooltipProvider>
  );
}

export function ClientTypeBadge({ type }: { type: OAuthApp["clientType"] }) {
  const confidential = type !== "public";
  return (
    <TooltipProvider delayDuration={300}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Badge variant="secondary" className="cursor-default gap-1">
            {confidential ? <ShieldCheckIcon aria-hidden /> : null}
            {confidential ? "Server app" : "Browser / mobile app"}
          </Badge>
        </TooltipTrigger>
        <TooltipContent className="max-w-64 text-pretty">
          {confidential
            ? "Confidential client: your server signs in with a client secret plus PKCE."
            : "Public client: no client secret; the app proves itself with PKCE alone."}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

export function AppAvatar({
  name,
  logoUrl,
  size = "default",
}: {
  name: string;
  logoUrl?: string | null;
  size?: "default" | "lg";
}) {
  return (
    <Avatar size={size} className={size === "default" ? "size-8" : undefined}>
      {logoUrl ? <AvatarImage src={logoUrl} alt="" /> : null}
      <AvatarFallback className={size === "default" ? "text-xs" : undefined}>
        {initials(name)}
      </AvatarFallback>
    </Avatar>
  );
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0]!.slice(0, 2).toUpperCase();
  return (parts[0]![0]! + parts[parts.length - 1]![0]!).toUpperCase();
}

/** A label / value row for a definition list (credentials, endpoints). Rows
 * sit in a `divide-y` list, not in their own bordered boxes. */
export function InfoRow({
  label,
  hint,
  children,
  action,
}: {
  label: ReactNode;
  hint?: ReactNode;
  children: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="grid gap-1 py-3 first:pt-0 last:pb-0 sm:grid-cols-[11rem_minmax(0,1fr)_auto] sm:items-center sm:gap-4">
      <div className="min-w-0">
        <div className="text-sm font-medium">{label}</div>
        {hint ? <div className="text-xs text-muted-foreground">{hint}</div> : null}
      </div>
      <div className="min-w-0">{children}</div>
      {action ? <div className="flex sm:justify-end">{action}</div> : null}
    </div>
  );
}

/** The copy-once modal shown after create OR rotate. Only the explicit
 * "I've saved it" button closes it; the caller drops the secret on close. */
export function SecretDialog({
  secret,
  clientId,
  onClose,
  rotated,
}: {
  secret: string | null;
  clientId?: string;
  onClose: () => void;
  rotated?: boolean;
}) {
  return (
    <SecretRevealDialog
      open={Boolean(secret)}
      secret={secret ?? ""}
      title={rotated ? "Your new client secret" : "Save your client secret"}
      description={
        <>
          Your app's server uses this secret, together with the client ID, to
          prove it is really your app. Store it in your server's environment
          (for example as <code className="font-mono">WA_CLIENT_SECRET</code>),
          never in browser code.
          {rotated ? " The previous secret has stopped working." : null}
        </>
      }
      secretLabel="Client secret"
      copyLabel="Copy secret"
      onClose={onClose}
    >
      {clientId ? (
        <div className="flex min-w-0 items-center justify-between gap-3 text-sm">
          <span className="text-muted-foreground">Client ID</span>
          <CopyableId value={clientId} label="Copy client ID" />
        </div>
      ) : null}
    </SecretRevealDialog>
  );
}
