import * as React from "react";
import { CheckIcon, CopyIcon } from "lucide-react";
import { Button } from "~/components/ui/button";
import { cn } from "~/lib/utils";

/** Copy-to-clipboard button with a transient "copied" tick. */
export function CopyButton({
  value,
  label = "Copy",
  copiedLabel = "Copied",
  liveMessage = "Copied to clipboard",
  className,
}: {
  value: string;
  label?: string;
  copiedLabel?: string;
  liveMessage?: string;
  className?: string;
}) {
  const [copied, setCopied] = React.useState(false);
  const timer = React.useRef<ReturnType<typeof setTimeout> | null>(null);

  React.useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);

  const onCopy = React.useCallback(() => {
    void navigator.clipboard?.writeText(value).then(() => {
      setCopied(true);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1600);
    });
  }, [value]);

  return (
    <><Button
      type="button"
      variant="outline"
      size="sm"
      onClick={onCopy}
      aria-label={copied ? copiedLabel : label}
      className={cn(className)}
    >
      {copied ? <CheckIcon aria-hidden /> : <CopyIcon aria-hidden />}
      {copied ? copiedLabel : label}
    </Button><span className="sr-only" aria-live="polite">{copied ? liveMessage : ""}</span></>
  );
}
