// Shared presentational bits for the OAuth-apps surface: status/mode/type badges
// and the copy-once client-secret modal (matches the API-key secret UX in
// user/keys.tsx — oauth.md §6.2 "shown exactly once").

import { KeyRoundIcon, ShieldCheckIcon } from "lucide-react";
import { Badge } from "~/components/ui/badge";
import { Button } from "~/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog";
import { CopyButton } from "../-components/user-ui";
import type { OAuthApp, OAuthMode } from "~/lib/api/hooks/oauth";

export function AppStatusBadge({ status }: { status: OAuthApp["status"] }) {
  return status === "active" ? (
    <Badge variant="default">active</Badge>
  ) : (
    <Badge variant="destructive">disabled</Badge>
  );
}

export function ModeChips({ modes }: { modes: OAuthMode[] | null | undefined }) {
  const list = modes ?? [];
  if (list.length === 0) {
    return <span className="text-xs text-muted-foreground">—</span>;
  }
  return (
    <div className="flex flex-wrap gap-1">
      {list.map((m) => (
        <Badge key={m} variant="outline" className="uppercase">
          {m}
        </Badge>
      ))}
    </div>
  );
}

export function ClientTypeBadge({ type }: { type: OAuthApp["clientType"] }) {
  return (
    <Badge variant="secondary" className="gap-1">
      {type === "public" ? null : <ShieldCheckIcon className="size-3" aria-hidden />}
      {type}
    </Badge>
  );
}

/** The copy-once modal shown after create OR rotate. The secret is held in the
 * caller's state only until dismissed. */
export function SecretDialog({
  secret,
  onClose,
  rotated,
}: {
  secret: string | null;
  onClose: () => void;
  rotated?: boolean;
}) {
  return (
    <Dialog open={Boolean(secret)} onOpenChange={(next) => !next && onClose()}>
      <DialogContent showCloseButton={false}>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <KeyRoundIcon className="size-5" aria-hidden />
            {rotated ? "Your new client secret" : "Save your client secret"}
          </DialogTitle>
          <DialogDescription>
            Copy it now — we only store a hash, so this is the only time it's
            shown.
            {rotated && " The previous secret stopped working immediately."}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3 py-2">
          <div className="rounded-md border bg-muted/40 p-3">
            <code className="block break-all text-sm">{secret}</code>
          </div>
          <CopyButton
            value={secret ?? ""}
            label="Copy secret"
            className="w-full"
          />
        </div>

        <DialogFooter>
          <Button onClick={onClose}>I've saved it</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
