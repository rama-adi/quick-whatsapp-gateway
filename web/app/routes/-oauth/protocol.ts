// Wire types + transport for the public "Sign in with WhatsApp" consent page.
//
// This is the END-USER facing side of the OIDC provider (docs/specs/oauth.md
// §6.1). It is deliberately independent of the authenticated gateway API client
// (app/lib/api/*): the /oauth/wait/* endpoints are PUBLIC and capability-scoped
// by the 160-bit browser code carried in the URL fragment — NO Bearer JWT, no
// cookies. The only thing shared with the rest of the app is the router origin
// (VITE_GATEWAY_URL); everything else here is self-contained so the consent page
// never accidentally attaches a caller identity.
//
// The pending-request snapshot and the status transition frames follow the exact
// shapes the router serves (oauth.md §2 step 3, §3.2). If the backend and this
// file ever disagree on a field, THIS is the contract the page renders against —
// keep them in sync (see the contract notes handed to the backend track).

/** Router origin (same env the API client uses), WITHOUT the /api/v1 suffix and
 * WITHOUT any auth headers — the /oauth/* endpoints are router-local + public. */
const ROUTER_ORIGIN = (import.meta.env.VITE_GATEWAY_URL ?? "").replace(/\/+$/, "");

/** Build a fully-qualified router URL for an /oauth path (leading "/"). */
export function oauthUrl(path: string): string {
  return `${ROUTER_ORIGIN}${path}`;
}

// ---------------------------------------------------------------------------
// Wire shapes (oauth.md §2, §4.2)
// ---------------------------------------------------------------------------

/** Verification target — how/where the end-user proves control. */
export type WaitTarget =
  | { mode: "dm"; number: string; bot_name?: string; group_name?: undefined }
  | { mode: "group"; group_name: string; number?: string; bot_name?: string };

export interface WaitApp {
  name: string;
  logo?: string | null;
}

/** The first frame on the stream: the full snapshot to render the consent card. */
export interface PendingSnapshot {
  status: "pending";
  app: WaitApp;
  user_code: string;
  /** The app's configured keyword, e.g. "login" or "masuk" (oauth.md §6.1). */
  login_command: string;
  target: WaitTarget;
  scopes: string[];
  /** Absolute expiry, epoch milliseconds (see contract notes in the report). */
  expires_at: number;
}

/** Terminal / heartbeat frames that tail the snapshot. `finalized` appears when
 * a stream (re)connects after the redirect already happened — the flow is spent
 * and the page must not resume it (oauth.md §6.1). */
export type StatusFrame =
  | PendingSnapshot
  | { status: "verified" }
  | { status: "denied" }
  | { status: "expired" }
  | { status: "finalized" }
  | { status: "heartbeat" };

/** Response of POST /oauth/wait/{code}/finalize. */
export interface FinalizeResponse {
  redirect: string;
}

// ---------------------------------------------------------------------------
// Frame classification (pure — unit-tested)
// ---------------------------------------------------------------------------

export function isPending(f: StatusFrame): f is PendingSnapshot {
  return f.status === "pending";
}
export function isHeartbeat(f: StatusFrame): f is { status: "heartbeat" } {
  return f.status === "heartbeat";
}
export function isTerminal(
  f: StatusFrame,
): f is { status: "verified" | "denied" | "expired" | "finalized" } {
  return (
    f.status === "verified" ||
    f.status === "denied" ||
    f.status === "expired" ||
    f.status === "finalized"
  );
}

// ---------------------------------------------------------------------------
// Reload detection (oauth.md §6.1 — refresh kills the attempt)
// ---------------------------------------------------------------------------

/**
 * Detect whether this page load is a RELOAD of a consent page that already ran
 * for this browser code in this tab. First sight stamps `loadId` under the
 * code's key; seeing a DIFFERENT stamp means an earlier page load in this tab
 * already owned the code (refresh / back-nav) — the caller must kill the
 * attempt. Re-running with the SAME loadId is a no-op (React StrictMode mounts
 * effects twice). Storage failures (blocked sessionStorage) fail open: never
 * killing is safer than killing every login.
 */
export function isReload(storage: Storage, browserCode: string, loadId: string): boolean {
  const key = `wa-login-load:${browserCode}`;
  try {
    const prior = storage.getItem(key);
    if (prior === null) {
      storage.setItem(key, loadId);
      return false;
    }
    return prior !== loadId;
  } catch {
    return false;
  }
}

// ---------------------------------------------------------------------------
// Fragment parsing
// ---------------------------------------------------------------------------

/** Extract the browser code from a location hash like "#c=<code>". Tolerates a
 * leading "#", key/value form, or a bare code. Returns null when absent. */
export function parseBrowserCode(hash: string): string | null {
  const raw = hash.replace(/^#/, "").trim();
  if (!raw) return null;
  // Support "c=<code>" (spec form) and "<code>" (bare) — nothing else.
  if (raw.includes("=")) {
    const params = new URLSearchParams(raw);
    const c = params.get("c");
    return c ? c.trim() || null : null;
  }
  return raw;
}

// ---------------------------------------------------------------------------
// NDJSON line-buffered stream reader
// ---------------------------------------------------------------------------

/**
 * Read an NDJSON body (one JSON object per line) from a ReadableStream of bytes,
 * invoking onFrame for each parsed object. Line-buffered: partial lines are held
 * until their terminating newline arrives. Malformed lines are skipped (they
 * never crash the reader). Resolves when the stream ends (EOF).
 *
 * Kept transport-agnostic (takes a reader, not a Response) so tests can feed a
 * synthetic ReadableStream. Aborting is the caller's job (AbortController on the
 * fetch); when aborted, reader.read() rejects and the rejection propagates.
 */
export async function readNdjson(
  reader: ReadableStreamDefaultReader<Uint8Array>,
  onFrame: (frame: StatusFrame) => void,
): Promise<void> {
  const decoder = new TextDecoder();
  let buf = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let nl: number;
    while ((nl = buf.indexOf("\n")) !== -1) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (!line) continue;
      const frame = tryParse(line);
      if (frame) onFrame(frame);
    }
  }
  // Flush any trailing line without a newline (e.g. a final frame on close).
  const tail = buf.trim();
  if (tail) {
    const frame = tryParse(tail);
    if (frame) onFrame(frame);
  }
}

function tryParse(line: string): StatusFrame | null {
  try {
    const obj = JSON.parse(line) as unknown;
    if (
      obj &&
      typeof obj === "object" &&
      typeof (obj as { status?: unknown }).status === "string"
    ) {
      return obj as StatusFrame;
    }
    return null;
  } catch {
    return null;
  }
}

// ---------------------------------------------------------------------------
// WhatsApp deep link + verification-message helpers
// ---------------------------------------------------------------------------

/** The exact text the end-user must send, e.g. "login 483920" / "masuk 483920". */
export function verificationMessage(loginCommand: string, userCode: string): string {
  return `${loginCommand} ${userCode}`;
}

/** wa.me deep link pre-filling the DM. `number` is a phone number in any human
 * format; wa.me wants digits only (no "+", spaces, or dashes). */
export function waMeLink(number: string, text: string): string {
  const digits = number.replace(/[^\d]/g, "");
  return `https://wa.me/${digits}?text=${encodeURIComponent(text)}`;
}
