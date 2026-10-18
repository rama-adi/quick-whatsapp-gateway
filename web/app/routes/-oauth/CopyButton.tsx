// Thin wrapper kept so the public consent card's call sites stay unchanged;
// the implementation lives in the shared ~/components/copyable.

import { CopyButton as SharedCopyButton } from "~/components/copyable";

/** Copy-to-clipboard button with a transient "copied" tick (shared CopyButton). */
export function CopyButton(props: {
  value: string;
  label?: string;
  copiedLabel?: string;
  liveMessage?: string;
  className?: string;
}) {
  return <SharedCopyButton {...props} />;
}
