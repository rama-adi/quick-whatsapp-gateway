import type { components } from "~/lib/api/schema";

type Gateway = components["schemas"]["GatewayAdmin"];
type Result = components["schemas"]["GatewayReconciliationResult"];

export function revisionGap(applied: number, desired: number): string {
  const gap = desired - applied;
  return gap === 0
    ? `${applied} applied / ${desired} desired (in sync)`
    : `${applied} applied / ${desired} desired (${Math.abs(gap)} revision${Math.abs(gap) === 1 ? "" : "s"} ${gap > 0 ? "behind" : "ahead"})`;
}

export function keystorePresence(gateway: Gateway): string {
  return gateway.keystorePresent === true
    ? "Present"
    : gateway.keystorePresent === false ? "Not present" : "Not reported";
}

export function keystoreBytes(gateway: Gateway): string {
  return gateway.keystoreBytes === undefined ? "Not reported" : formatBytes(gateway.keystoreBytes);
}

export function journalPressure(gateway: Gateway): string {
  if (gateway.journalState === undefined) return "Not reported";
  const entries = gateway.journalEntries ?? 0;
  const bytes = gateway.journalBytes ?? 0;
  return `${gateway.journalState} · ${entries} pending · ${formatBytes(bytes)}`;
}

export function reconciliationSubject(result: Result): string {
  // An unexpected device has no trustworthy session/org assignment. Do not
  // infer either from local state or a malformed response field.
  return result.status === "unexpected_local_device"
    ? "Unassigned local device"
    : result.sessionId ? `Session ${result.sessionId}` : "No assigned session";
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}
