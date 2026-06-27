// Local presentational helpers for the viewer surface (avatars, timestamp
// formatting, structured-message parsing). Kept inside the surface dir per the
// contract; report in sharedGaps for the verify phase to hoist if reused.

import { Avatar, AvatarFallback } from "~/components/ui/avatar";
import type { Chat, Message } from "~/lib/api/types";

/** Compact avatar with initials derived from a chat/contact name or JID. */
export function ChatAvatar({
  name,
  jid,
  type,
}: {
  name?: string;
  jid?: string;
  type?: Chat["type"];
}) {
  const label = name || jid || "?";
  const initials = deriveInitials(label);
  const isGroup = type === "group" || type === "broadcast";
  return (
    <Avatar className="size-9 shrink-0">
      <AvatarFallback
        className={isGroup ? "bg-primary/15 text-primary" : undefined}
        aria-hidden="true"
      >
        {initials}
      </AvatarFallback>
    </Avatar>
  );
}

function deriveInitials(label: string): string {
  const cleaned = label.replace(/@.*$/, "").trim();
  if (!cleaned) return "?";
  const parts = cleaned.split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) {
    const single = parts[0] ?? "";
    return single.slice(0, 2).toUpperCase();
  }
  return ((parts[0]?.[0] ?? "") + (parts[1]?.[0] ?? "")).toUpperCase();
}

/** Human-friendly timestamp: time today, otherwise short date. */
export function formatTimestamp(ms?: number): string {
  if (!ms) return "";
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return "";
  const now = new Date();
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  if (sameDay) {
    return d.toLocaleTimeString(undefined, {
      hour: "2-digit",
      minute: "2-digit",
    });
  }
  const sameYear = d.getFullYear() === now.getFullYear();
  return d.toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    ...(sameYear ? {} : { year: "numeric" }),
  });
}

/** Day separator label for grouping a timeline. */
export function formatDayHeading(ms?: number): string {
  if (!ms) return "";
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return "";
  const now = new Date();
  const startOf = (x: Date) =>
    new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diffDays = Math.round((startOf(now) - startOf(d)) / 86_400_000);
  if (diffDays === 0) return "Today";
  if (diffDays === 1) return "Yesterday";
  return d.toLocaleDateString(undefined, {
    weekday: "short",
    month: "short",
    day: "numeric",
    ...(d.getFullYear() === now.getFullYear() ? {} : { year: "numeric" }),
  });
}

export function dayKey(ms?: number): string {
  if (!ms) return "unknown";
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return "unknown";
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
}

/** Media message types that are not downloadable in v1 (server returns 501). */
const MEDIA_TYPES = new Set([
  "image",
  "video",
  "audio",
  "document",
  "sticker",
  "ptt",
  "voice",
]);

export type ParsedMessage =
  | { kind: "text"; text: string }
  | { kind: "media"; mediaType: string; caption?: string }
  | { kind: "poll"; name?: string; options: string[] }
  | { kind: "location"; latitude?: number; longitude?: number; name?: string }
  | { kind: "contact"; name?: string; phone?: string }
  | { kind: "system" }
  | { kind: "unknown"; type?: string; text?: string };

/**
 * Normalize a Message into a renderable shape. The API delivers `type` plus a
 * `body` string; for structured types `body` may be a JSON blob, so we attempt
 * a tolerant parse and fall back to the raw text.
 */
export function parseMessage(m: Message): ParsedMessage {
  const type = (m.type ?? "text").toLowerCase();
  const body = m.body ?? "";
  const struct = tryParseJson(body);

  if (type === "text" || type === "chat" || type === "") {
    return { kind: "text", text: body };
  }

  if (type === "system") {
    // Content-less group/protocol notices. The gateway drops these going
    // forward; legacy rows render as nothing (their body is raw event JSON).
    return { kind: "system" };
  }

  if (MEDIA_TYPES.has(type)) {
    const caption =
      (struct && typeof struct.caption === "string" && struct.caption) ||
      (body && !struct ? body : undefined);
    return { kind: "media", mediaType: type, caption: caption || undefined };
  }

  if (type === "poll") {
    const name = struct && typeof struct.name === "string" ? struct.name : undefined;
    const options =
      struct && Array.isArray(struct.options)
        ? struct.options.filter((o): o is string => typeof o === "string")
        : [];
    return {
      kind: "poll",
      name: name ?? (options.length === 0 ? body || undefined : undefined),
      options,
    };
  }

  if (type === "location") {
    return {
      kind: "location",
      latitude: struct && typeof struct.latitude === "number" ? struct.latitude : undefined,
      longitude:
        struct && typeof struct.longitude === "number" ? struct.longitude : undefined,
      name:
        struct && typeof struct.name === "string"
          ? struct.name
          : body && !struct
            ? body
            : undefined,
    };
  }

  if (type === "contact" || type === "vcard") {
    return {
      kind: "contact",
      name: struct && typeof struct.name === "string" ? struct.name : undefined,
      phone: struct && typeof struct.phone === "string" ? struct.phone : undefined,
    };
  }

  return { kind: "unknown", type: m.type, text: body || undefined };
}

/**
 * Cross-kind extras carried in the message body JSON: a quoted/reply preview
 * and reaction emoji. The realtime path patches `body` on reaction/edit events
 * (cacheBridge), so these surface from the same tolerant parse. All optional —
 * absent fields render nothing.
 */
export type MessageExtras = {
  quoted?: { author?: string; preview: string };
  reactions: string[];
  edited: boolean;
};

export function parseExtras(m: Message): MessageExtras {
  const struct = tryParseJson(m.body ?? "");
  const quotedRaw = struct?.quoted;
  let quoted: MessageExtras["quoted"];
  if (quotedRaw && typeof quotedRaw === "object") {
    const q = quotedRaw as Record<string, unknown>;
    const preview =
      typeof q.text === "string"
        ? q.text
        : typeof q.body === "string"
          ? q.body
          : undefined;
    if (preview) {
      quoted = {
        author: typeof q.author === "string" ? q.author : undefined,
        preview,
      };
    }
  }
  const reactions = Array.isArray(struct?.reactions)
    ? (struct.reactions as unknown[]).filter(
        (r): r is string => typeof r === "string",
      )
    : [];
  return { quoted, reactions, edited: struct?.edited === true };
}

function tryParseJson(s: string): Record<string, unknown> | null {
  const t = s.trim();
  if (!t.startsWith("{") && !t.startsWith("[")) return null;
  try {
    const v: unknown = JSON.parse(t);
    return v && typeof v === "object" ? (v as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}
