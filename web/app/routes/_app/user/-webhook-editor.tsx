import type { Dispatch, SetStateAction } from "react";
import { PlusIcon, XIcon } from "lucide-react";
import type { Webhook, WebhookRequest } from "~/lib/api/types";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";

const EVENT_CATALOG = [
  "session.status",
  "auth.qr",
  "auth.code",
  "message",
  "message.from_me",
  "message.status",
  "message.reaction",
  "message.interactive_reply",
  "message.edited",
  "message.revoked",
  "poll.vote",
  "presence.update",
  "group.update",
  "group.participant",
  "chat.update",
  "contact.update",
  "call.incoming",
  "newsletter.update",
] as const;


// --- shared form state ----------------------------------------------------

export type HeaderRow = { key: string; value: string };

export type FormState = {
  url: string;
  sessionId: string;
  allEvents: boolean;
  events: Set<string>;
  secret: string;
  headers: HeaderRow[];
  active: boolean;
};

export function emptyForm(): FormState {
  return {
    url: "",
    sessionId: "",
    allEvents: true,
    events: new Set(),
    secret: "",
    headers: [],
    active: true,
  };
}

export function formFromWebhook(w: Webhook): FormState {
  const events = w.events ?? [];
  const allEvents = events.length === 0 || events.includes("*");
  return {
    url: w.url ?? "",
    sessionId: w.sessionId ?? "",
    allEvents,
    events: new Set(allEvents ? [] : events),
    // secret is write-only and never returned; leave blank on edit.
    secret: "",
    headers: Object.entries(w.customHeaders ?? {}).map(([key, value]) => ({
      key,
      value,
    })),
    active: w.active !== false,
  };
}

// Build the WebhookRequest body, omitting blank optional fields so a PATCH
// doesn't clobber server-held write-only values (secret) it can't echo back.
export function toRequest(s: FormState): WebhookRequest {
  const body: WebhookRequest = {
    url: s.url.trim(),
    events: s.allEvents ? ["*"] : [...s.events],
    active: s.active,
  };
  const sessionId = s.sessionId.trim();
  if (sessionId) body.sessionId = sessionId;
  const secret = s.secret.trim();
  if (secret) body.secret = secret;
  const headers = s.headers.filter((h) => h.key.trim());
  if (headers.length > 0) {
    body.customHeaders = Object.fromEntries(
      headers.map((h) => [h.key.trim(), h.value]),
    );
  }
  return body;
}

export function validateWebhookForm(s: FormState): string | null {
  const url = s.url.trim();
  if (!url) return "Enter a delivery URL.";
  try {
    const parsed = new URL(url);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return "URL must use http or https.";
    }
  } catch {
    return "Enter a valid URL.";
  }
  if (!s.allEvents && s.events.size === 0) {
    return "Select at least one event, or choose all events.";
  }
  if (s.headers.some((h) => !h.key.trim() && h.value.trim())) {
    return "Custom header rows need a name.";
  }
  const headerNames = s.headers
    .map((header) => header.key.trim().toLowerCase())
    .filter(Boolean);
  if (new Set(headerNames).size !== headerNames.length) {
    return "Custom header names must be unique.";
  }
  if (
    headerNames.some(
      (name) => name === "content-type" || name.startsWith("x-webhook-"),
    )
  ) {
    return "Content-Type and X-Webhook-* headers are managed by the gateway.";
  }
  return null;
}

export function WebhookForm({
  state,
  setState,
  idPrefix,
}: {
  state: FormState;
  setState: Dispatch<SetStateAction<FormState>>;
  idPrefix: string;
}) {
  const toggleEvent = (e: string, checked: boolean): void =>
    setState((cur) => {
      const events = new Set(cur.events);
      if (checked) events.add(e);
      else events.delete(e);
      return { ...cur, events };
    });

  return (
    <div className="space-y-4 py-4">
      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-url`}>Delivery URL</Label>
        <Input
          id={`${idPrefix}-url`}
          type="url"
          value={state.url}
          onChange={(e) => setState((c) => ({ ...c, url: e.target.value }))}
          placeholder="https://example.com/webhooks/wa"
          autoFocus
        />
      </div>

      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-session`}>Session ID (optional)</Label>
        <Input
          id={`${idPrefix}-session`}
          value={state.sessionId}
          onChange={(e) =>
            setState((c) => ({ ...c, sessionId: e.target.value }))
          }
          placeholder="Leave blank for all your sessions"
          className="font-mono text-xs"
        />
      </div>

      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">Events</legend>
        <label className="flex items-center gap-3 text-sm">
          <input
            type="checkbox"
            className="size-4 accent-primary"
            checked={state.allEvents}
            onChange={(e) =>
              setState((c) => ({ ...c, allEvents: e.target.checked }))
            }
          />
          <span>All events</span>
        </label>
        {!state.allEvents && (
          <div className="grid max-h-48 grid-cols-1 gap-1.5 overflow-y-auto rounded-md border p-3 sm:grid-cols-2">
            {EVENT_CATALOG.map((e) => (
              <label
                key={e}
                htmlFor={`${idPrefix}-evt-${e}`}
                className="flex items-center gap-2 text-sm"
              >
                <input
                  id={`${idPrefix}-evt-${e}`}
                  type="checkbox"
                  className="size-4 accent-primary"
                  checked={state.events.has(e)}
                  onChange={(ev) => toggleEvent(e, ev.target.checked)}
                />
                <span className="font-mono text-xs">{e}</span>
              </label>
            ))}
          </div>
        )}
      </fieldset>

      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-secret`}>HMAC secret (optional)</Label>
        <Input
          id={`${idPrefix}-secret`}
          type="password"
          value={state.secret}
          onChange={(e) => setState((c) => ({ ...c, secret: e.target.value }))}
          placeholder="Used to sign delivery payloads"
          autoComplete="off"
        />
      </div>

      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">
          Custom headers (optional)
        </legend>
        <div className="space-y-2">
          {state.headers.map((h, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input
                value={h.key}
                onChange={(e) =>
                  setState((c) => ({
                    ...c,
                    headers: c.headers.map((row, j) =>
                      j === i ? { ...row, key: e.target.value } : row,
                    ),
                  }))
                }
                placeholder="Header name"
                aria-label="Header name"
              />
              <Input
                value={h.value}
                onChange={(e) =>
                  setState((c) => ({
                    ...c,
                    headers: c.headers.map((row, j) =>
                      j === i ? { ...row, value: e.target.value } : row,
                    ),
                  }))
                }
                placeholder="Value"
                aria-label="Header value"
              />
              <Button
                type="button"
                size="icon"
                variant="ghost"
                aria-label="Remove header"
                onClick={() =>
                  setState((c) => ({
                    ...c,
                    headers: c.headers.filter((_, j) => j !== i),
                  }))
                }
              >
                <XIcon className="size-4" aria-hidden />
              </Button>
            </div>
          ))}
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="gap-1.5"
            onClick={() =>
              setState((c) => ({
                ...c,
                headers: [...c.headers, { key: "", value: "" }],
              }))
            }
          >
            <PlusIcon className="size-4" aria-hidden />
            Add header
          </Button>
        </div>
      </fieldset>

      <label className="flex items-center gap-3 text-sm">
        <input
          type="checkbox"
          className="size-4 accent-primary"
          checked={state.active}
          onChange={(e) =>
            setState((c) => ({ ...c, active: e.target.checked }))
          }
        />
        <span>Enabled</span>
      </label>
    </div>
  );
}
