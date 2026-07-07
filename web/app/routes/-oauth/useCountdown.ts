import * as React from "react";

/** Milliseconds remaining until `expiresAt` (epoch ms), ticking each second.
 * Returns 0 once elapsed. `null` expiry disables the timer. */
export function useCountdown(expiresAt: number | null): number {
  const compute = React.useCallback(
    () => (expiresAt == null ? 0 : Math.max(0, expiresAt - Date.now())),
    [expiresAt],
  );
  const [remaining, setRemaining] = React.useState(compute);

  React.useEffect(() => {
    if (expiresAt == null) return;
    setRemaining(compute());
    const id = setInterval(() => setRemaining(compute()), 1000);
    return () => clearInterval(id);
  }, [expiresAt, compute]);

  return remaining;
}

/** Format ms remaining as "m:ss". */
export function formatCountdown(ms: number): string {
  const total = Math.ceil(ms / 1000);
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${s.toString().padStart(2, "0")}`;
}
