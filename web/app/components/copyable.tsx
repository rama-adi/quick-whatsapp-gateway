// Copy-to-clipboard primitives: the one CopyButton (text or icon-only) and
// CopyableId (a mono, optionally middle-truncated ID with an icon copy button).
// Works outside the app shell too (the tooltip brings its own provider), and
// falls back to execCommand where the async Clipboard API is unavailable
// (plain-http self-hosted dashboards).

import { useCallback, useEffect, useRef, useState, type ComponentProps } from "react";
import { CheckIcon, CopyIcon } from "lucide-react";
import { Button } from "~/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "~/components/ui/tooltip";
import { middleTruncate } from "~/lib/format";
import { cn } from "~/lib/utils";

/** Write text to the clipboard; resolves false when the browser refuses. */
export async function copyText(value: string): Promise<boolean> {
  try {
    if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value);
      return true;
    }
  } catch {
    // Fall through to the legacy path (e.g. a denied permission).
  }
  if (typeof document === "undefined") return false;
  const el = document.createElement("textarea");
  el.value = value;
  el.setAttribute("readonly", "");
  el.style.position = "fixed";
  el.style.opacity = "0";
  document.body.appendChild(el);
  el.select();
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    el.remove();
  }
}

/** Clipboard state hook: `copy(value)` plus a `copied` flag that resets after `resetMs`. */
export function useCopyToClipboard(resetMs = 1600): {
  copied: boolean;
  copy: (value: string) => Promise<boolean>;
} {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);
  const copy = useCallback(
    async (value: string) => {
      const ok = await copyText(value);
      setCopied(ok);
      if (timer.current) clearTimeout(timer.current);
      if (ok) timer.current = setTimeout(() => setCopied(false), resetMs);
      return ok;
    },
    [resetMs],
  );
  return { copied, copy };
}

export interface CopyButtonProps {
  /** The text written to the clipboard. */
  value: string;
  /** Button text (or tooltip + aria-label when `iconOnly`). Default "Copy". */
  label?: string;
  /** Feedback text after a successful copy. Default "Copied". */
  copiedLabel?: string;
  /** Screen-reader announcement after a copy. Default "Copied to clipboard". */
  liveMessage?: string;
  /** Render a small square icon button with a tooltip instead of icon + text. */
  iconOnly?: boolean;
  /** Button variant. Default "outline" (text) / "ghost" (icon-only). */
  variant?: ComponentProps<typeof Button>["variant"];
  /** Button size. Default "sm" (text) / "icon-xs" (icon-only). */
  size?: ComponentProps<typeof Button>["size"];
  disabled?: boolean;
  className?: string;
  /** Called after a successful copy. */
  onCopied?: () => void;
}

/** The shared copy button: icon + label (default) or icon-only with a tooltip. */
export function CopyButton({
  value,
  label = "Copy",
  copiedLabel = "Copied",
  liveMessage = "Copied to clipboard",
  iconOnly = false,
  variant,
  size,
  disabled,
  className,
  onCopied,
}: CopyButtonProps) {
  const { copied, copy } = useCopyToClipboard();
  const onClick = () => {
    void copy(value).then((ok) => ok && onCopied?.());
  };
  const solid = (variant ?? (iconOnly ? "ghost" : "outline")) === "default";
  const icon = copied ? (
    <CheckIcon
      aria-hidden
      className={solid ? undefined : "text-emerald-600 dark:text-emerald-400"}
    />
  ) : (
    <CopyIcon aria-hidden />
  );
  const live = (
    <span className="sr-only" aria-live="polite">
      {copied ? liveMessage : ""}
    </span>
  );

  if (iconOnly) {
    return (
      <TooltipProvider delayDuration={300}>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              type="button"
              variant={variant ?? "ghost"}
              size={size ?? "icon-xs"}
              disabled={disabled || !value}
              onClick={onClick}
              aria-label={copied ? copiedLabel : label}
              className={cn("text-muted-foreground hover:text-foreground", className)}
            >
              {icon}
            </Button>
          </TooltipTrigger>
          <TooltipContent>{copied ? copiedLabel : label}</TooltipContent>
        </Tooltip>
        {live}
      </TooltipProvider>
    );
  }

  return (
    <>
      <Button
        type="button"
        variant={variant ?? "outline"}
        size={size ?? "sm"}
        disabled={disabled || !value}
        onClick={onClick}
        aria-label={copied ? copiedLabel : label}
        className={className}
      >
        {icon}
        {copied ? copiedLabel : label}
      </Button>
      {live}
    </>
  );
}

/** A mono ID with an inline copy button; `truncate` shortens long IDs in the middle. */
export function CopyableId({
  value,
  display,
  truncate = false,
  label = "Copy ID",
  className,
}: {
  /** The full ID (always what gets copied and shown on hover). */
  value: string;
  /** Override the visible text (e.g. a formatted phone number). */
  display?: string;
  /** `true` keeps 8 head + 6 tail chars; `[head, tail]` customizes it. */
  truncate?: boolean | readonly [number, number];
  /** Tooltip / aria-label for the copy button. Default "Copy ID". */
  label?: string;
  className?: string;
}) {
  const shown =
    display ??
    (truncate === true
      ? middleTruncate(value)
      : Array.isArray(truncate)
        ? middleTruncate(value, truncate[0], truncate[1])
        : value);
  return (
    <span
      data-slot="copyable-id"
      className={cn("inline-flex max-w-full min-w-0 items-center gap-0.5", className)}
    >
      <span title={value} className="min-w-0 truncate font-mono text-xs">
        {shown}
      </span>
      <CopyButton value={value} label={label} iconOnly />
    </span>
  );
}
