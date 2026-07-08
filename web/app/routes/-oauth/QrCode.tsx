// A dependency-light QR renderer. `uqr` (zero-dependency, ~5kB) encodes the
// wa.me deep link and returns an inline SVG string — no <canvas>, no external
// asset, no network. That keeps the consent page self-contained and CSP-clean
// (oauth.md §6.1: no third-party scripts).

import * as React from "react";
import { renderSVG } from "uqr";
import { cn } from "~/lib/utils";

export interface QrCodeProps {
  /** The URL/text to encode. */
  value: string;
  /** Rendered pixel size (square). */
  size?: number;
  className?: string;
}

export function QrCode({ value, size = 200, className }: QrCodeProps) {
  const svg = React.useMemo(
    () =>
      renderSVG(value, {
        // High-contrast, quiet-zone padded; colors track the current theme via
        // currentColor is not supported by uqr, so we pass explicit hex that
        // reads on the white card in both themes (the QR sits on a white plate).
        blackColor: "#0a0a0a",
        whiteColor: "#ffffff",
        border: 2,
      }),
    [value],
  );

  return (
    <div
      className={cn(
        "inline-flex items-center justify-center rounded-lg bg-white p-3 shadow-sm ring-1 ring-black/5",
        className,
      )}
      style={{ width: size, height: size }}
      // uqr output is a static, self-generated SVG string (no user HTML) — safe.
      dangerouslySetInnerHTML={{ __html: sizeSvg(svg, size - 24) }}
      aria-hidden
    />
  );
}

/** Force the generated <svg> to the target pixel box (uqr sizes by modules). */
function sizeSvg(svg: string, px: number): string {
  return svg.replace(
    /<svg /,
    `<svg width="${px}" height="${px}" style="display:block" `,
  );
}
