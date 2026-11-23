// The session view model: one explicit state per session, derived from the
// merged session row (lib/sessions/merge.ts) plus the pairing artifacts the
// cache holds (QR code, phone pairing code, passkey step, failure reason).
//
// Panels switch on `kind`. A pending lifecycle action is an overlay the hook
// returns next to the view; it never changes `kind`.
//
// To swap inputs later (a failureReason on the REST row, a side-effect-free
// GET /qr with an explicit pending/ready answer, a status sequence), change
// SessionViewInput and its builder in useSessionView.ts; deriveSessionView's
// rules stay the same.

import type {
  PasskeyStep,
  QRCode,
  SessionFailureReason,
  WASession,
} from "../api/types";

/** A code the user acts on, with the epoch-ms instant it stops working (if known). */
export type PairingArtifact = { code: string; expiresAt?: number };

/** The live artifacts of the current pairing attempt, already filtered by expiry. */
export type PairingArtifacts = { qr?: PairingArtifact; pairingCode?: PairingArtifact };

export type SessionView =
  | { kind: "loading" }
  /** The row could not be loaded and nothing is cached. */
  | { kind: "unreachable"; error: unknown }
  | { kind: "stopped"; paired: boolean }
  /** status starting: connecting, or reconnecting when paired. */
  | { kind: "connecting"; paired: boolean }
  /** scan_qr_code with no live code yet (or the last one expired). */
  | { kind: "awaiting_pairing"; artifacts: PairingArtifacts }
  | { kind: "qr"; code: string; expiresAt?: number; artifacts: PairingArtifacts }
  | { kind: "pairing_code"; code: string; expiresAt?: number; artifacts: PairingArtifacts }
  | { kind: "passkey"; step: PasskeyStep }
  /** working, but the attached identity (waJid) has not loaded yet. */
  | { kind: "linking"; stalled: boolean }
  | { kind: "working" }
  | { kind: "failed"; reason?: SessionFailureReason }
  | { kind: "logged_out" };

export type SessionViewKind = SessionView["kind"];

export interface SessionViewInput {
  /** The merged session row, if any is cached. */
  session: WASession | undefined;
  /** True while the first load is in flight and nothing is cached. */
  loading: boolean;
  /** The load error, when the load failed. Ignored while a row is cached. */
  error: unknown;
  qr: QRCode | undefined;
  pairingCode: PairingArtifact | undefined;
  passkey: PasskeyStep | undefined;
  failure: SessionFailureReason | undefined;
  /** When the session was first seen working without an identity (epoch ms). */
  linkingSince: number | undefined;
  now: number;
}

/** How long "linking" may last before the view offers a retry. */
export const LINKING_STALL_MS = 10_000;

/** An artifact is live while it has a code and has not expired. */
export function liveArtifact(
  artifact: PairingArtifact | undefined,
  now: number,
): PairingArtifact | undefined {
  if (!artifact?.code) return undefined;
  if (artifact.expiresAt !== undefined && now >= artifact.expiresAt) return undefined;
  return artifact;
}

export function deriveSessionView(input: SessionViewInput): SessionView {
  const row = input.session;
  if (!row) {
    if (input.error) return { kind: "unreachable", error: input.error };
    return { kind: "loading" };
  }
  const paired = Boolean(row.waJid);
  switch (row.status) {
    case "stopped":
      return { kind: "stopped", paired };
    case "starting":
      return { kind: "connecting", paired };
    case "failed":
      return { kind: "failed", reason: input.failure };
    case "logged_out":
      return { kind: "logged_out" };
    case "working":
      if (paired) return { kind: "working" };
      return {
        kind: "linking",
        stalled:
          input.linkingSince !== undefined &&
          input.now - input.linkingSince >= LINKING_STALL_MS,
      };
    case "scan_qr_code": {
      // Artifacts count only while the session awaits pairing; cacheBridge
      // also drops them on any other status.
      if (input.passkey) return { kind: "passkey", step: input.passkey };
      const artifacts: PairingArtifacts = {
        qr: liveArtifact(input.qr, input.now),
        pairingCode: liveArtifact(input.pairingCode, input.now),
      };
      // A requested phone code is the method the user chose; it outranks the
      // QR the same attempt keeps rotating.
      if (artifacts.pairingCode) {
        return { kind: "pairing_code", ...artifacts.pairingCode, artifacts };
      }
      if (artifacts.qr) return { kind: "qr", ...artifacts.qr, artifacts };
      return { kind: "awaiting_pairing", artifacts };
    }
    default:
      return { kind: "stopped", paired };
  }
}

/** The pairing artifacts of a view, empty outside the pairing states. */
export function viewArtifacts(view: SessionView): PairingArtifacts {
  return "artifacts" in view ? view.artifacts : {};
}

/** True while the session waits for the phone: a code is shown or coming. */
export function isAwaitingPairing(view: SessionView): boolean {
  return (
    view.kind === "awaiting_pairing" ||
    view.kind === "qr" ||
    view.kind === "pairing_code" ||
    view.kind === "passkey"
  );
}

/**
 * The next instant the view could change by time alone: an artifact expiring
 * or linking stalling. The hook re-renders then.
 */
export function nextViewDeadline(input: SessionViewInput): number | undefined {
  const times: number[] = [];
  if (input.session?.status === "scan_qr_code") {
    for (const a of [input.qr, input.pairingCode]) {
      if (a?.expiresAt !== undefined && a.expiresAt > input.now) times.push(a.expiresAt);
    }
  }
  if (input.linkingSince !== undefined) {
    const stall = input.linkingSince + LINKING_STALL_MS;
    if (stall > input.now) times.push(stall);
  }
  return times.length ? Math.min(...times) : undefined;
}
