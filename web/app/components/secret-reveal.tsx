// One-time secret reveal (API keys, OAuth client secrets, enrollment tokens).
// The secret is shown with a copy button and a "won't be shown again"
// warning. Esc, outside clicks and the close X are disabled: the only way out
// is the explicit "I've saved it" button, so a stray click can't lose it.
//
//   <SecretRevealDialog
//     open={Boolean(created)}
//     secret={created?.key ?? ""}
//     title="Save your API key"
//     copyLabel="Copy key"
//     onClose={() => setCreated(null)}
//   >
//     <CodeSnippet tabs={apiRequestSnippets({ url, token: created.key })} />
//   </SecretRevealDialog>

import type { ReactNode } from "react";
import { KeyRoundIcon, TriangleAlertIcon } from "lucide-react";
import { Button } from "~/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog";
import { CopyButton } from "~/components/copyable";
import { cn } from "~/lib/utils";

/** Modal that reveals a one-time secret and can only be closed by acknowledging it. */
export function SecretRevealDialog({
  open,
  secret,
  title = "Save this secret",
  description,
  secretLabel,
  copyLabel = "Copy",
  acknowledgeLabel = "I've saved it",
  icon,
  children,
  className,
  onClose,
}: {
  open: boolean;
  /** The one-time secret (may be multi-line, e.g. an env block). */
  secret: string;
  title?: ReactNode;
  /** Extra context under the title. The "shown only once" warning is always added. */
  description?: ReactNode;
  /** Small caption above the secret box, e.g. "API key". */
  secretLabel?: ReactNode;
  /** Copy button text. Default "Copy". */
  copyLabel?: string;
  /** Dismiss button text. Default "I've saved it". */
  acknowledgeLabel?: string;
  /** Title icon. Default a key. */
  icon?: ReactNode;
  /** Extra content below the secret (next steps, a curl snippet, …). */
  children?: ReactNode;
  /** DialogContent classes (e.g. "sm:max-w-2xl" when showing snippets). */
  className?: string;
  /** Called only from the acknowledge button. */
  onClose: () => void;
}) {
  return (
    <Dialog open={open}>
      <DialogContent
        showCloseButton={false}
        className={cn("max-h-[calc(100svh-2rem)] overflow-y-auto sm:max-w-xl", className)}
        onEscapeKeyDown={(e) => e.preventDefault()}
        onInteractOutside={(e) => e.preventDefault()}
      >
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            {icon ?? <KeyRoundIcon className="size-5 text-muted-foreground" aria-hidden />}
            {title}
          </DialogTitle>
          <DialogDescription className="text-pretty">
            {description ?? "Copy it now and store it somewhere safe, like a password manager or your server's environment variables."}
          </DialogDescription>
        </DialogHeader>

        <div className="grid min-w-0 gap-2">
          {secretLabel ? (
            <div className="text-xs font-medium text-muted-foreground">{secretLabel}</div>
          ) : null}
          <div className="flex min-w-0 items-start gap-2 rounded-xl border bg-muted/40 p-3 dark:bg-input/20">
            <pre className="min-w-0 flex-1 font-mono text-sm break-all whitespace-pre-wrap select-all">
              {secret}
            </pre>
          </div>
          <CopyButton value={secret} label={copyLabel} size="default" className="w-full" />
          <p className="flex items-start gap-2 rounded-lg bg-amber-500/10 px-3 py-2 text-xs text-amber-800 dark:text-amber-300">
            <TriangleAlertIcon className="mt-px size-3.5 shrink-0" aria-hidden />
            This is the only time it is shown. If you lose it, you'll need to create a new one.
          </p>
        </div>

        {children ? <div className="grid min-w-0 gap-3">{children}</div> : null}

        <DialogFooter>
          <Button onClick={onClose}>{acknowledgeLabel}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
