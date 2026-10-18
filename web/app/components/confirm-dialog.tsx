// Promise-based confirmation dialog that replaces window.confirm.
//
// <ConfirmProvider> is mounted once in AppShell; any page calls useConfirm():
//
//   const confirm = useConfirm();
//   if (!(await confirm({
//     title: <>Revoke “{key.name}”?</>,
//     description: "Apps using this key stop working immediately.",
//     confirmLabel: "Revoke key",
//     destructive: true,
//   }))) return;
//
// For very destructive actions pass `confirmText` (e.g. the session name): the
// confirm button stays disabled until the user types it exactly. Esc, a click
// outside, or Cancel resolve `false`. Focus starts on Cancel (or the input).

import {
  createContext,
  useCallback,
  useContext,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Button } from "~/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog";
import { Input } from "~/components/ui/input";

export interface ConfirmOptions {
  /** Question that names the item, e.g. `Delete “Sales line”?`. */
  title: ReactNode;
  /** What happens next and whether it can be undone. */
  description?: ReactNode;
  /** Confirm button text; use the verb ("Delete session"). Default "Confirm". */
  confirmLabel?: string;
  /** Cancel button text. Default "Cancel". */
  cancelLabel?: string;
  /** Style the confirm button as destructive. */
  destructive?: boolean;
  /** Require typing this exact text before confirming (very destructive actions). */
  confirmText?: string;
}

type ConfirmFn = (options: ConfirmOptions) => Promise<boolean>;

const ConfirmContext = createContext<ConfirmFn | null>(null);

interface Pending {
  options: ConfirmOptions;
  resolve: (ok: boolean) => void;
}

/** Hosts the single confirm dialog; mount once near the app root. */
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [pending, setPending] = useState<Pending | null>(null);
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const pendingRef = useRef<Pending | null>(null);

  const settle = useCallback((ok: boolean) => {
    pendingRef.current?.resolve(ok);
    pendingRef.current = null;
    setOpen(false);
  }, []);

  const confirm = useCallback<ConfirmFn>((options) => {
    // A second confirm while one is open cancels the first.
    pendingRef.current?.resolve(false);
    return new Promise<boolean>((resolve) => {
      const next = { options, resolve };
      pendingRef.current = next;
      setPending(next);
      setTyped("");
      setOpen(true);
    });
  }, []);

  const opts = pending?.options;
  const needsText = Boolean(opts?.confirmText);
  const canConfirm = !needsText || typed.trim() === opts?.confirmText?.trim();
  const cancelRef = useRef<HTMLButtonElement>(null);

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      <Dialog open={open} onOpenChange={(next) => !next && settle(false)}>
        <DialogContent
          showCloseButton={false}
          className="sm:max-w-md"
          onOpenAutoFocus={(e) => {
            if (needsText) return; // let the input take focus
            e.preventDefault();
            cancelRef.current?.focus();
          }}
        >
          {opts && (
            <form
              className="grid gap-4"
              onSubmit={(e) => {
                e.preventDefault();
                if (canConfirm) settle(true);
              }}
            >
              <DialogHeader>
                <DialogTitle className="text-pretty">{opts.title}</DialogTitle>
                {opts.description ? (
                  <DialogDescription className="text-pretty">
                    {opts.description}
                  </DialogDescription>
                ) : (
                  <DialogDescription className="sr-only">
                    Confirm this action.
                  </DialogDescription>
                )}
              </DialogHeader>

              {needsText && (
                <div className="grid gap-2">
                  <label htmlFor="confirm-text" className="text-sm text-muted-foreground">
                    Type{" "}
                    <span className="font-mono font-medium text-foreground select-all">
                      {opts.confirmText}
                    </span>{" "}
                    to confirm.
                  </label>
                  <Input
                    id="confirm-text"
                    value={typed}
                    onChange={(e) => setTyped(e.target.value)}
                    autoComplete="off"
                    spellCheck={false}
                    autoFocus
                  />
                </div>
              )}

              <DialogFooter>
                <Button
                  ref={cancelRef}
                  type="button"
                  variant="outline"
                  onClick={() => settle(false)}
                >
                  {opts.cancelLabel ?? "Cancel"}
                </Button>
                <Button
                  type="submit"
                  variant={opts.destructive ? "destructive" : "default"}
                  disabled={!canConfirm}
                >
                  {opts.confirmLabel ?? "Confirm"}
                </Button>
              </DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </ConfirmContext.Provider>
  );
}

/** Returns `confirm(options)`, which resolves true only when the user confirms. */
export function useConfirm(): ConfirmFn {
  const confirm = useContext(ConfirmContext);
  if (!confirm) {
    throw new Error("useConfirm must be used inside <ConfirmProvider> (mounted in AppShell)");
  }
  return confirm;
}
