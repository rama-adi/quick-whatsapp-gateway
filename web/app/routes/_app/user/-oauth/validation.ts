// Pure validation + URL-building helpers for the OAuth-apps editor and the
// integration-guide tab (oauth.md §6.2, §4.4). Kept side-effect-free so the
// non-trivial logic is unit-tested independently of React.

// ---------------------------------------------------------------------------
// Login command (oauth.md §5.1: single word, [a-z0-9_-]{2,32}, case-insensitive)
// ---------------------------------------------------------------------------

const LOGIN_COMMAND_RE = /^[a-z0-9_-]{2,32}$/;

export function validateLoginCommand(raw: string): string | null {
  const value = raw.trim();
  if (!value) return "Enter a login command.";
  if (/[A-Z]/.test(value)) return "Use lowercase only.";
  if (/\s/.test(value)) return "Must be a single word (no spaces).";
  if (value.length < 2) return "At least 2 characters.";
  if (value.length > 32) return "At most 32 characters.";
  if (!LOGIN_COMMAND_RE.test(value)) {
    return "Only lowercase letters, digits, hyphen and underscore.";
  }
  return null;
}

// ---------------------------------------------------------------------------
// Redirect URIs (oauth.md §1, §4.2: absolute https, exact; localhost http ok for
// dev; no fragments). Exact-match set — no wildcards.
// ---------------------------------------------------------------------------

const LOCAL_HOSTS = new Set(["localhost", "127.0.0.1", "[::1]", "::1"]);

/** Validate one redirect URI. Returns an error string, or null if valid. */
export function validateRedirectUri(raw: string): string | null {
  const value = raw.trim();
  if (!value) return "Enter a redirect URI.";

  let url: URL;
  try {
    url = new URL(value);
  } catch {
    return "Not a valid absolute URL.";
  }

  if (url.hash) return "Fragments (#...) are not allowed.";

  const isLocal = LOCAL_HOSTS.has(url.hostname.toLowerCase());
  if (url.protocol === "https:") {
    // https is always fine.
  } else if (url.protocol === "http:") {
    if (!isLocal) return "http is only allowed for localhost (use https).";
  } else {
    return "Must be an http (localhost) or https URL.";
  }

  return null;
}

export interface RedirectUriIssues {
  /** Per-index error, or null if that row is valid. */
  perUri: (string | null)[];
  /** Indices that duplicate an earlier identical URI. */
  duplicates: Set<number>;
  /** True when every non-empty URI is valid and there's at least one. */
  ok: boolean;
}

/** Validate a whole list, flagging exact duplicates (the set is exact-match, so
 * a repeat is dead weight, not an error the backend needs — but we warn). */
export function validateRedirectUris(uris: string[]): RedirectUriIssues {
  const perUri = uris.map((u) => (u.trim() ? validateRedirectUri(u) : null));
  const duplicates = new Set<number>();
  const seen = new Map<string, number>();
  uris.forEach((u, i) => {
    const key = u.trim();
    if (!key) return;
    if (seen.has(key)) duplicates.add(i);
    else seen.set(key, i);
  });
  const nonEmpty = uris.filter((u) => u.trim());
  const ok =
    nonEmpty.length > 0 &&
    perUri.every((e) => e === null) &&
    duplicates.size === 0;
  return { perUri, duplicates, ok };
}

/** Collapse the editor's URI rows to the clean set the API expects: trimmed,
 * non-empty, de-duplicated, order-preserving. */
export function normalizeRedirectUris(uris: string[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const u of uris) {
    const v = u.trim();
    if (!v || seen.has(v)) continue;
    seen.add(v);
    out.push(v);
  }
  return out;
}

// ---------------------------------------------------------------------------
// TTL clamping (oauth.md §7.7: access/id_token default 900s; refresh default 30d)
// ---------------------------------------------------------------------------

export const TOKEN_TTL = { min: 60, max: 86_400, default: 900 } as const; // 1m–1d
export const REFRESH_TTL = {
  min: 3_600,
  max: 7_776_000,
  default: 2_592_000,
} as const; // 1h–90d, default 30d

export function clamp(value: number, min: number, max: number): number {
  if (Number.isNaN(value)) return min;
  return Math.min(max, Math.max(min, Math.round(value)));
}

/** Human "30 days" / "15 minutes" / "1 hour" from a second count. */
export function humanizeSeconds(seconds: number): string {
  if (seconds % 86_400 === 0) return plural(seconds / 86_400, "day");
  if (seconds % 3_600 === 0) return plural(seconds / 3_600, "hour");
  if (seconds % 60 === 0) return plural(seconds / 60, "minute");
  return plural(seconds, "second");
}

function plural(n: number, unit: string): string {
  return `${n} ${unit}${n === 1 ? "" : "s"}`;
}

// ---------------------------------------------------------------------------
// Authorize-URL builder for the integration guide (oauth.md §2 step 1)
// ---------------------------------------------------------------------------

export interface AuthorizeParams {
  issuer: string; // router origin, e.g. https://gw.example.com
  clientId: string;
  redirectUri: string;
  scopes: string[];
  /** Optional acr_values ("wa:dm" | "wa:group") when the app enables both. */
  acrValues?: string;
}

/** Build the exact GET /oauth/authorize URL a relying app would hit. Uses
 * placeholders for the values only the client library knows at runtime (state,
 * PKCE) so the guide shows a realistic, copy-pasteable shape. */
export function buildAuthorizeUrl(p: AuthorizeParams): string {
  const base = trimSlash(p.issuer);
  const query = new URLSearchParams({
    response_type: "code",
    client_id: p.clientId,
    redirect_uri: p.redirectUri,
    scope: p.scopes.join(" "),
    state: "<state>",
    code_challenge: "<code_challenge>",
    code_challenge_method: "S256",
  });
  if (p.acrValues) query.set("acr_values", p.acrValues);
  return `${base}/oauth/authorize?${query.toString()}`;
}

export function discoveryUrl(issuer: string): string {
  return `${trimSlash(issuer)}/.well-known/openid-configuration`;
}

function trimSlash(s: string): string {
  return s.replace(/\/+$/, "");
}
