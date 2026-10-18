// Pure presentation helpers for the gateway admin pages: human labels for the
// registry's raw enums, which lifecycle actions are valid in each state
// (mirrors internal/store/gateway_admin_tx.go), capability parsing and the
// delete preconditions. No React here so the rules stay unit-testable.

import type { components } from "~/lib/api/schema";

type Gateway = components["schemas"]["GatewayAdmin"];
type GatewayDetail = components["schemas"]["GatewayAdminDetail"];
type Result = components["schemas"]["GatewayReconciliationResult"];
export type GatewayStatus = Gateway["status"];

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
  return `${JOURNAL_LABEL[gateway.journalState] ?? gateway.journalState} · ${entries} pending · ${formatBytes(bytes)}`;
}

const JOURNAL_LABEL: Record<NonNullable<Gateway["journalState"]>, string> = {
  healthy: "Healthy",
  degraded: "Degraded",
  paused: "Paused (disk pressure)",
  critical: "Critical",
};

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

// --- Status vocabulary -------------------------------------------------------

export type Tone = "ok" | "warn" | "info" | "muted" | "bad";

export interface StatusMeta {
  label: string;
  description: string;
  tone: Tone;
}

export const GATEWAY_STATUS: Record<GatewayStatus, StatusMeta> = {
  pending_enrollment: {
    label: "Waiting to enroll",
    description: "Created, but no gateway process has redeemed its enrollment token yet.",
    tone: "info",
  },
  joining: {
    label: "Joining",
    description: "Enrolled and connecting to the API; not yet taking sessions.",
    tone: "warn",
  },
  active: {
    label: "Active",
    description: "Connected and accepting new sessions.",
    tone: "ok",
  },
  draining: {
    label: "Draining",
    description: "Keeps its current sessions but takes no new ones.",
    tone: "warn",
  },
  drained: {
    label: "Drained",
    description: "Takes no new sessions. Safe to re-enroll or delete once its sessions are moved.",
    tone: "muted",
  },
  degraded: {
    label: "Degraded",
    description: "Connected but reporting a problem (keystore, journal or reconciliation).",
    tone: "bad",
  },
  disabled: {
    label: "Disabled",
    description: "The API rejects this gateway's connection until it is re-enabled.",
    tone: "muted",
  },
};

export function gatewayStatusMeta(status: string | undefined): StatusMeta {
  return (
    GATEWAY_STATUS[status as GatewayStatus] ?? {
      label: status ? humanize(status) : "Unknown",
      description: "",
      tone: "muted",
    }
  );
}

export const RECONCILIATION_STATUS: Record<Gateway["reconciliationStatus"], StatusMeta> = {
  pending: { label: "Pending", description: "Waiting for the gateway to report its devices.", tone: "info" },
  healthy: { label: "In sync", description: "Every assigned session matches a local device.", tone: "ok" },
  degraded: { label: "Out of sync", description: "Some sessions or devices don't match; see the results below.", tone: "bad" },
};

export const RESULT_STATUS: Record<Result["status"], StatusMeta> = {
  applied: { label: "Applied", description: "The device matches its assigned session.", tone: "ok" },
  keystore_missing: { label: "Keystore missing", description: "The session's keys are missing on this gateway.", tone: "bad" },
  keystore_corrupt: { label: "Keystore corrupt", description: "The session's keys failed an integrity check.", tone: "bad" },
  unexpected_local_device: { label: "Unexpected device", description: "A local device has no session assigned to this gateway.", tone: "warn" },
};

export const LIFECYCLE_LABEL: Record<Gateway["desiredLifecycle"], string> = {
  run: "Accepting new sessions",
  drain: "Draining (no new sessions)",
};

export const CONNECTION_MODE_LABEL: Record<Gateway["connectionMode"], string> = {
  control: "Control plane (mTLS)",
  legacy: "Legacy",
};

/** "enrollment.succeeded" / "gateway_drained" → "Enrollment succeeded". */
export function humanize(value: string): string {
  const words = value.replace(/[._-]+/g, " ").trim().toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

// --- Lifecycle transitions ----------------------------------------------------

export type GatewayActionName = "drain" | "resume" | "disable" | "reenable" | "reenroll" | "replaceToken";

/**
 * Which lifecycle actions the API accepts for this gateway right now. Mirrors
 * the store's preconditions so the page offers only valid transitions.
 */
export function availableActions(gateway: Pick<Gateway, "status" | "enrolledAt">): Set<GatewayActionName> {
  const enrolled = Boolean(gateway.enrolledAt);
  const s = gateway.status;
  const out = new Set<GatewayActionName>();
  if (enrolled && (s === "joining" || s === "active" || s === "degraded")) out.add("drain");
  if (enrolled && (s === "draining" || s === "drained")) out.add("resume");
  if (s !== "disabled") out.add("disable");
  if (s === "disabled") out.add("reenable");
  if (enrolled && (s === "drained" || s === "disabled")) out.add("reenroll");
  if (s === "pending_enrollment") out.add("replaceToken");
  return out;
}

// --- Capabilities ---------------------------------------------------------------

const CAPABILITY_LABEL: Record<string, string> = {
  session_engine: "Session engine",
  event_stream: "Event stream",
  command_execution: "Command execution",
};
const CAPABILITY_BY_NUMBER: Record<number, string> = {
  1: "session_engine",
  2: "event_stream",
  3: "command_execution",
};

function capabilityName(raw: unknown): string | null {
  if (typeof raw === "number") {
    const known = CAPABILITY_BY_NUMBER[raw];
    return known ? CAPABILITY_LABEL[known]! : `Capability ${raw}`;
  }
  if (typeof raw !== "string" || !raw.trim()) return null;
  const key = raw.trim().toLowerCase().replace(/^gateway_capability_/, "");
  if (key === "unknown") return null;
  return CAPABILITY_LABEL[key] ?? humanize(key);
}

/**
 * Turn the opaque `capabilities` JSON the gateway reports into readable
 * labels. Accepts a list (`["GATEWAY_CAPABILITY_EVENT_STREAM", 1]`), an object
 * of flags (`{ eventStream: true }`) or an object of values.
 */
export function capabilityLabels(capabilities: unknown): string[] {
  if (capabilities === null || capabilities === undefined) return [];
  if (Array.isArray(capabilities)) {
    return capabilities.map(capabilityName).filter((v): v is string => Boolean(v));
  }
  if (typeof capabilities === "object") {
    const out: string[] = [];
    for (const [key, value] of Object.entries(capabilities as Record<string, unknown>)) {
      if (value === false || value === null || value === undefined) continue;
      const name = capabilityName(key.replace(/([a-z0-9])([A-Z])/g, "$1_$2")) ?? key;
      out.push(value === true ? name : `${name}: ${typeof value === "object" ? JSON.stringify(value) : String(value)}`);
    }
    return out;
  }
  const single = capabilityName(capabilities);
  return single ? [single] : [];
}

// --- Delete preconditions ---------------------------------------------------------

export interface DeleteCheck {
  label: string;
  /** true = satisfied, false = blocks delete, null = verified by the API on delete. */
  ok: boolean | null;
  hint?: string;
}

/** The API's delete preconditions, evaluated from the detail response where possible. */
export function deleteChecks(detail: GatewayDetail, now = Date.now()): DeleteCheck[] {
  const { gateway } = detail;
  const quiesced = gateway.status === "drained" || gateway.status === "disabled";
  const liveCerts = (detail.certificates ?? []).filter((c) => !c.revokedAt);
  return [
    {
      label: "Drained or disabled",
      ok: quiesced,
      hint: quiesced ? undefined : "Drain it and wait for it to finish, or disable it.",
    },
    {
      label: "No assigned sessions",
      ok: detail.assignedSessionCount === 0,
      hint:
        detail.assignedSessionCount === 0
          ? undefined
          : `${detail.assignedSessionCount} session${detail.assignedSessionCount === 1 ? " is" : "s are"} still assigned here.`,
    },
    {
      label: "No unrevoked certificate",
      ok: liveCerts.length === 0,
      hint:
        liveCerts.length === 0
          ? undefined
          : liveCerts.some((c) => c.notAfter > now)
            ? "Its certificate is still valid. Re-enrolling revokes it."
            : "An expired certificate is still unrevoked. Re-enrolling revokes it.",
    },
    {
      label: "No pending enrollment token",
      ok: null,
      hint: "Checked by the API when you delete.",
    },
  ];
}
