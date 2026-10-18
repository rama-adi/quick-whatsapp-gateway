// One vocabulary for times, roles, enums and long IDs across the dashboard.
// Times are always rendered in the viewer's LOCAL time zone. Pair these with
// <Time> (components/time.tsx) when the value is shown in the UI, so the
// absolute timestamp is always one hover away from a relative label.

/** A timestamp as the API and better-auth hand it to us. */
export type DateInput = number | string | Date | null | undefined;

/** Normalize epoch millis/seconds, ISO strings and Dates to a valid Date (or null). */
export function toDate(value: DateInput): Date | null {
  if (value === null || value === undefined || value === "") return null;
  let d: Date;
  if (value instanceof Date) {
    d = value;
  } else if (typeof value === "number") {
    if (value <= 0) return null;
    // The API emits int64 epoch millis; tolerate seconds just in case.
    d = new Date(value < 1e12 ? value * 1000 : value);
  } else {
    d = new Date(value);
  }
  return Number.isNaN(d.getTime()) ? null : d;
}

const DATE_TIME = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});
const DATE_TIME_SECONDS = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "medium",
});
const DATE_ONLY = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" });
const MONTH_DAY = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
});

/** Absolute local date + time, e.g. "Oct 18, 2026, 3:04 PM"; `fallback` when empty. */
export function formatDateTime(
  value: DateInput,
  { fallback = "—", seconds = false }: { fallback?: string; seconds?: boolean } = {},
): string {
  const d = toDate(value);
  if (!d) return fallback;
  return (seconds ? DATE_TIME_SECONDS : DATE_TIME).format(d);
}

/** Absolute local date only, e.g. "Oct 18, 2026"; `fallback` when empty. */
export function formatDate(value: DateInput, { fallback = "—" }: { fallback?: string } = {}): string {
  const d = toDate(value);
  return d ? DATE_ONLY.format(d) : fallback;
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** Relative label, e.g. "just now", "3 min ago", "in 2 hr", "yesterday", "Oct 3". */
export function formatRelative(
  value: DateInput,
  { fallback = "—", now = Date.now() }: { fallback?: string; now?: number } = {},
): string {
  const d = toDate(value);
  if (!d) return fallback;
  const diff = now - d.getTime();
  const abs = Math.abs(diff);
  const future = diff < 0;
  const ago = (n: number, unit: string) => (future ? `in ${n} ${unit}` : `${n} ${unit} ago`);

  if (abs < 45_000) return future ? "in a moment" : "just now";
  if (abs < HOUR) return ago(Math.max(1, Math.round(abs / MINUTE)), "min");
  if (abs < DAY) return ago(Math.round(abs / HOUR), "hr");
  const days = Math.round(abs / DAY);
  if (days === 1) return future ? "tomorrow" : "yesterday";
  if (days < 7) return ago(days, "days");
  return d.getFullYear() === new Date(now).getFullYear()
    ? MONTH_DAY.format(d)
    : DATE_ONLY.format(d);
}

/** Shorten a long ID by keeping its head and tail, e.g. "a1b2c3d4…f9e8d7". */
export function middleTruncate(value: string, head = 8, tail = 6): string {
  if (value.length <= head + tail + 1) return value;
  return `${value.slice(0, head)}…${value.slice(-tail)}`;
}

/** Turn a raw enum value ("desired_lifecycle", "scan-qr") into "Desired lifecycle". */
export function humanizeEnum(value: string | null | undefined, fallback = "—"): string {
  if (!value) return fallback;
  const words = value.replace(/[_-]+/g, " ").trim().toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

const PLATFORM_ROLE_LABEL: Record<string, string> = {
  super_admin: "Platform admin",
  admin: "Admin",
  user: "Member",
};

/** Human label for a better-auth platform role ("super_admin" → "Platform admin"). */
export function formatPlatformRole(role: string | null | undefined): string {
  if (!role) return "Member";
  return PLATFORM_ROLE_LABEL[role] ?? humanizeEnum(role);
}

/** "628123456789" → "+628123456789"; undefined/empty → undefined. */
export function formatPhone(digits: string | undefined | null): string | undefined {
  if (!digits) return undefined;
  const d = digits.replace(/[^\d]/g, "");
  return d ? `+${d}` : undefined;
}

/** Fallback title for a session with no label and no linked number. */
export const UNTITLED_SESSION = "Untitled session";

/**
 * The human title for a WhatsApp session, used everywhere a session is named
 * (top bar, overview heading, lists, pickers, confirmations): its label, else
 * its linked phone number, else "Untitled session". Never the raw `sess_…` ID;
 * the overview keeps that one copy-click away as "Session ID".
 */
export function sessionTitle(s: {
  label?: string | null;
  phoneNumber?: string | null;
}): string {
  return s.label?.trim() || formatPhone(s.phoneNumber) || UNTITLED_SESSION;
}
