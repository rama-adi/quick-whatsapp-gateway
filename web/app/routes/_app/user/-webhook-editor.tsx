// Webhook create/edit form: state, validation, request building, and the form
// UI shared by the create and edit dialogs in webhooks.tsx.
//
// Progressive disclosure: the default form is URL + session scope + events.
// The signing secret, custom headers and retry policy sit under "Advanced".
// The HMAC secret and the custom headers' values are write-only on the server
// side for the secret (never returned), so an edit only sends a secret when the
// user explicitly chooses "Replace secret".

import { useId, type Dispatch, type ReactNode, type SetStateAction } from "react";
import {
  ArrowUpRightIcon,
  ChevronRightIcon,
  PlusIcon,
  SparklesIcon,
  XIcon,
} from "lucide-react";
import type { Webhook, WebhookRequest } from "~/lib/api/types";
import { useSessions } from "~/lib/api/hooks/sessions";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "~/components/ui/select";
import { CopyButton } from "~/components/copyable";
import { DOCS } from "~/lib/docs-links";
import { cn } from "~/lib/utils";

// --- event catalog --------------------------------------------------------

export type EventInfo = { name: string; title: string; description: string };
export type EventGroup = { id: string; title: string; events: EventInfo[] };

/** Every event type the gateway emits, grouped for the picker. Mirrors the
 * catalog in backend/internal/domain/event.go and the event-shape guide. */
export const EVENT_GROUPS: EventGroup[] = [
  {
    id: "messages",
    title: "Messages",
    events: [
      { name: "message", title: "New message", description: "Someone sends a message to this number." },
      { name: "message.from_me", title: "Message sent by you", description: "A message is sent from this number, including from another linked device." },
      { name: "message.status", title: "Delivery receipts", description: "A message you sent is delivered, read or played." },
      { name: "message.reaction", title: "Reactions", description: "Someone adds or removes a reaction." },
      { name: "message.edited", title: "Message edited", description: "A message is edited." },
      { name: "message.revoked", title: "Message deleted", description: "A message is deleted for everyone." },
      { name: "message.interactive_reply", title: "Button or list reply", description: "Someone taps a reply button or picks a list option." },
    ],
  },
  {
    id: "polls",
    title: "Polls",
    events: [
      { name: "poll.vote", title: "Poll vote", description: "Someone votes on a poll." },
      { name: "poll.recap", title: "Poll results", description: "A poll's end time is reached (sent once, with the totals)." },
    ],
  },
  {
    id: "media",
    title: "Attachments",
    events: [
      { name: "media.ready", title: "Attachment stored", description: "An attachment finished uploading to your storage bucket. Includes its URL." },
      { name: "media.expired", title: "Attachment expired", description: "An attachment reached its retention limit and was deleted." },
    ],
  },
  {
    id: "session",
    title: "Connection",
    events: [
      { name: "session.status", title: "Session status", description: "The number connects, disconnects, fails or is logged out." },
      { name: "auth.qr", title: "New QR code", description: "A new QR code is ready to scan." },
      { name: "auth.code", title: "Pairing succeeded", description: "The number was linked successfully." },
    ],
  },
  {
    id: "people",
    title: "Chats, groups and contacts",
    events: [
      { name: "chat.update", title: "Chat updated", description: "A chat changes (currently profile-picture changes)." },
      { name: "contact.update", title: "Contact updated", description: "A contact's name or details change." },
      { name: "presence.update", title: "Online and typing", description: "A contact goes online or offline, or starts typing." },
      { name: "group.update", title: "Group updated", description: "A group's name, description or settings change." },
      { name: "group.participant", title: "Group members", description: "Someone joins or leaves a group, or changes role." },
    ],
  },
  {
    id: "other",
    title: "Calls and channels",
    events: [
      { name: "call.incoming", title: "Incoming call", description: "Someone calls this number." },
      { name: "newsletter.update", title: "Channel update", description: "A followed channel posts or changes." },
    ],
  },
];

const EVENT_INFO = new Map(
  EVENT_GROUPS.flatMap((g) => g.events.map((e) => [e.name, e] as const)),
);

/** Human title for an event type ("message.status" → "Delivery receipts"). */
export function eventTitle(name: string): string {
  return EVENT_INFO.get(name)?.title ?? name;
}

// --- retry policy ---------------------------------------------------------

export type RetryDraft = {
  attempts: string;
  delaySeconds: string;
  policy: "exponential" | "constant";
};

/** Starting values when the user customizes retries. Matches the shipped
 * gateway default (WEBHOOK_RETRIES_ATTEMPTS=5, WEBHOOK_RETRIES_DELAY=2); an
 * operator can change the real default, which is why create omits the policy
 * unless the user customizes it. */
export const DEFAULT_RETRY: RetryDraft = {
  attempts: "5",
  delaySeconds: "2",
  policy: "exponential",
};

const MAX_ATTEMPTS = 50;
const MAX_DELAY_SECONDS = 86_400;

/** Seconds from the first failed delivery until the last retry, or null when
 * the draft is invalid. Exponential waits delay × 2^(n-1) before retry n. */
export function lastRetryAfterSeconds(r: RetryDraft): number | null {
  const attempts = Number(r.attempts);
  const delay = Number(r.delaySeconds);
  if (!Number.isInteger(attempts) || !Number.isInteger(delay)) return null;
  if (attempts < 1 || delay < 1) return null;
  const retries = attempts - 1;
  if (r.policy === "exponential") return delay * (2 ** retries - 1);
  return delay * retries;
}

/** "45 s" / "8 min" / "9.1 h" / "3 days" — rough, for a hint line. */
export function formatDuration(seconds: number): string {
  if (seconds < 60) return `${seconds} s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)} min`;
  if (seconds < 86_400) {
    const h = seconds / 3600;
    return `${h < 10 ? h.toFixed(1).replace(/\.0$/, "") : Math.round(h)} h`;
  }
  const d = seconds / 86_400;
  if (d > 3650) return "over 10 years";
  return `${d < 10 ? d.toFixed(1).replace(/\.0$/, "") : Math.round(d)} days`;
}

// --- shared form state ----------------------------------------------------

export type HeaderRow = { key: string; value: string };

export type FormState = {
  url: string;
  /** "" = all of the organization's sessions. */
  sessionId: string;
  allEvents: boolean;
  events: Set<string>;
  /** On edit, only sent when `replaceSecret` is on. */
  secret: string;
  replaceSecret: boolean;
  headers: HeaderRow[];
  active: boolean;
  /** false = omit retryPolicy and keep the gateway default (create only). */
  customRetry: boolean;
  retry: RetryDraft;
};

export type FieldErrors = Partial<
  Record<"url" | "events" | "headers" | "secret" | "attempts" | "delaySeconds", string>
>;

export function emptyForm(): FormState {
  return {
    url: "",
    sessionId: "",
    allEvents: true,
    events: new Set(),
    secret: "",
    replaceSecret: true,
    headers: [],
    active: true,
    customRetry: false,
    retry: { ...DEFAULT_RETRY },
  };
}

export function formFromWebhook(w: Webhook): FormState {
  const events = w.events ?? [];
  const allEvents = events.includes("*");
  const rp = w.retryPolicy;
  return {
    url: w.url ?? "",
    sessionId: w.sessionId ?? "",
    allEvents,
    events: new Set(allEvents ? [] : events),
    // The secret is write-only and never returned; keep it unless replaced.
    secret: "",
    replaceSecret: false,
    headers: Object.entries(w.customHeaders ?? {}).map(([key, value]) => ({
      key,
      value,
    })),
    active: w.active !== false,
    customRetry: Boolean(rp),
    retry: rp
      ? {
          attempts: String(rp.attempts || DEFAULT_RETRY.attempts),
          delaySeconds: String(rp.delaySeconds || DEFAULT_RETRY.delaySeconds),
          policy: rp.policy === "exponential" ? "exponential" : "constant",
        }
      : { ...DEFAULT_RETRY },
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
  if (s.replaceSecret && secret) body.secret = secret;
  const headers = s.headers.filter((h) => h.key.trim());
  if (headers.length > 0) {
    body.customHeaders = Object.fromEntries(
      headers.map((h) => [h.key.trim(), h.value]),
    );
  }
  if (s.customRetry) {
    body.retryPolicy = {
      attempts: Number(s.retry.attempts),
      delaySeconds: Number(s.retry.delaySeconds),
      policy: s.retry.policy,
    };
  }
  return body;
}

/** Per-field validation errors (empty object when the form is valid). */
export function webhookFieldErrors(s: FormState): FieldErrors {
  const errors: FieldErrors = {};
  const url = s.url.trim();
  if (!url) {
    errors.url = "Enter a delivery URL.";
  } else {
    try {
      const parsed = new URL(url);
      if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
        errors.url = "URL must use http or https.";
      }
    } catch {
      errors.url = "Enter a valid URL, starting with https://.";
    }
  }
  if (!s.allEvents && s.events.size === 0) {
    errors.events = "Select at least one event, or choose all events.";
  }
  const headerNames = s.headers
    .map((header) => header.key.trim().toLowerCase())
    .filter(Boolean);
  if (s.headers.some((h) => !h.key.trim() && h.value.trim())) {
    errors.headers = "Custom header rows need a name.";
  } else if (new Set(headerNames).size !== headerNames.length) {
    errors.headers = "Custom header names must be unique.";
  } else if (
    headerNames.some(
      (name) => name === "content-type" || name.startsWith("x-webhook-"),
    )
  ) {
    errors.headers =
      "Content-Type and X-Webhook-* headers are managed by the gateway.";
  }
  if (s.replaceSecret && s.secret !== s.secret.trim()) {
    errors.secret = "Remove spaces from the start and end of the secret.";
  }
  if (s.customRetry) {
    const attempts = Number(s.retry.attempts);
    if (
      !s.retry.attempts.trim() ||
      !Number.isInteger(attempts) ||
      attempts < 1 ||
      attempts > MAX_ATTEMPTS
    ) {
      errors.attempts = `Enter a whole number from 1 to ${MAX_ATTEMPTS}.`;
    }
    const delay = Number(s.retry.delaySeconds);
    if (
      !s.retry.delaySeconds.trim() ||
      !Number.isInteger(delay) ||
      delay < 1 ||
      delay > MAX_DELAY_SECONDS
    ) {
      errors.delaySeconds = `Enter whole seconds from 1 to ${MAX_DELAY_SECONDS.toLocaleString()}.`;
    }
  }
  return errors;
}

/** The first validation error as a sentence, or null when valid. */
export function validateWebhookForm(s: FormState): string | null {
  const errors = webhookFieldErrors(s);
  return (
    errors.url ??
    errors.events ??
    errors.headers ??
    errors.secret ??
    errors.attempts ??
    errors.delaySeconds ??
    null
  );
}

/** True when an error lives inside the collapsed "Advanced" section. */
export function hasAdvancedError(errors: FieldErrors): boolean {
  return Boolean(
    errors.headers || errors.secret || errors.attempts || errors.delaySeconds,
  );
}

/** A random signing secret: "whsec_" + 32 random bytes as hex. */
export function generateSecret(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  return `whsec_${Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")}`;
}

// --- form UI --------------------------------------------------------------

const ALL_SESSIONS = "__all__";

export function WebhookForm({
  state,
  setState,
  mode,
  errors,
  advancedOpen,
  onAdvancedOpenChange,
}: {
  state: FormState;
  setState: Dispatch<SetStateAction<FormState>>;
  mode: "create" | "edit";
  /** Errors to show inline (pass {} until the first submit attempt). */
  errors: FieldErrors;
  advancedOpen: boolean;
  onAdvancedOpenChange: (open: boolean) => void;
}) {
  const id = useId();
  const set = <K extends keyof FormState>(key: K, value: FormState[K]) =>
    setState((c) => ({ ...c, [key]: value }));

  return (
    <div className="grid gap-5 py-4">
      <Field
        label="Delivery URL"
        htmlFor={`${id}-url`}
        error={errors.url}
        hint="Your server's HTTPS endpoint. Each event arrives as a JSON POST."
      >
        <Input
          id={`${id}-url`}
          type="url"
          inputMode="url"
          value={state.url}
          onChange={(e) => set("url", e.target.value)}
          placeholder="https://example.com/webhooks/whatsapp"
          aria-invalid={Boolean(errors.url) || undefined}
          autoFocus={mode === "create"}
          spellCheck={false}
        />
      </Field>

      <SessionScopeField
        id={`${id}-session`}
        value={state.sessionId}
        onChange={(v) => set("sessionId", v)}
      />

      <EventsField
        id={id}
        state={state}
        setState={setState}
        error={errors.events}
      />

      {mode === "edit" ? (
        <label
          htmlFor={`${id}-active`}
          className="flex cursor-pointer items-start gap-3 rounded-lg border bg-muted/30 px-3 py-2.5"
        >
          <input
            id={`${id}-active`}
            type="checkbox"
            className="mt-0.5 size-4 accent-primary"
            checked={state.active}
            onChange={(e) => set("active", e.target.checked)}
          />
          <span className="grid gap-0.5">
            <span className="text-sm font-medium">Deliver events</span>
            <span className="text-xs text-muted-foreground">
              Turn off to pause deliveries without deleting the webhook.
            </span>
          </span>
        </label>
      ) : null}

      <Disclosure
        open={advancedOpen}
        onOpenChange={onAdvancedOpenChange}
        title="Advanced"
        summary="Signing secret, custom headers, retries"
        attention={hasAdvancedError(errors)}
      >
        <SecretField
          id={`${id}-secret`}
          mode={mode}
          state={state}
          setState={setState}
          error={errors.secret}
        />
        <HeadersField state={state} setState={setState} error={errors.headers} />
        <RetryField
          id={`${id}-retry`}
          mode={mode}
          state={state}
          setState={setState}
          errors={errors}
        />
      </Disclosure>
    </div>
  );
}

function SessionScopeField({
  id,
  value,
  onChange,
}: {
  id: string;
  value: string;
  onChange: (sessionId: string) => void;
}) {
  const sessions = useSessions();
  const rows = sessions.data?.pages.flatMap((p) => p.data) ?? [];
  const unknown = value && !rows.some((s) => s.id === value);

  return (
    <Field
      label="Sessions"
      htmlFor={id}
      hint="Send events from every number in this organization, or just one."
    >
      <Select
        value={value || ALL_SESSIONS}
        onValueChange={(v) => onChange(v === ALL_SESSIONS ? "" : v)}
      >
        <SelectTrigger id={id} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent position="popper">
          <SelectItem value={ALL_SESSIONS}>All sessions</SelectItem>
          {rows.length > 0 || unknown ? <SelectSeparator /> : null}
          {rows.map((s) => (
            <SelectItem key={s.id} value={s.id}>
              <span className="truncate">{s.label || "Untitled session"}</span>
              <span className="text-muted-foreground">
                {s.phoneNumber ? `+${s.phoneNumber}` : "not linked yet"}
              </span>
            </SelectItem>
          ))}
          {unknown ? (
            <SelectItem value={value}>
              <span className="font-mono text-xs">{value}</span>
            </SelectItem>
          ) : null}
          {sessions.isLoading ? (
            <div className="px-2 py-1.5 text-xs text-muted-foreground">
              Loading sessions…
            </div>
          ) : null}
        </SelectContent>
      </Select>
    </Field>
  );
}

function EventsField({
  id,
  state,
  setState,
  error,
}: {
  id: string;
  state: FormState;
  setState: Dispatch<SetStateAction<FormState>>;
  error?: string;
}) {
  const setEvents = (update: (events: Set<string>) => void) =>
    setState((cur) => {
      const events = new Set(cur.events);
      update(events);
      return { ...cur, events };
    });

  // Event names on an existing webhook that this catalog doesn't know about:
  // keep them selectable so an edit never silently drops them.
  const extra = [...state.events].filter((e) => !EVENT_INFO.has(e));
  const groups: EventGroup[] = extra.length
    ? [
        ...EVENT_GROUPS,
        {
          id: "unknown",
          title: "Other",
          events: extra.map((name) => ({ name, title: name, description: "" })),
        },
      ]
    : EVENT_GROUPS;

  return (
    <fieldset className="grid gap-2" aria-describedby={error ? `${id}-events-error` : undefined}>
      <div className="flex items-baseline justify-between gap-3">
        <legend className="text-sm font-medium">Events</legend>
        <a
          href={DOCS.eventShape}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-0.5 text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
        >
          What each event contains
          <ArrowUpRightIcon className="size-3" aria-hidden />
        </a>
      </div>

      <div role="radiogroup" aria-label="Which events" className="grid gap-2 sm:grid-cols-2">
        <ChoiceCard
          name={`${id}-events-mode`}
          checked={state.allEvents}
          onSelect={() => setState((c) => ({ ...c, allEvents: true }))}
          title="All events"
          description="Everything, including event types added later."
        />
        <ChoiceCard
          name={`${id}-events-mode`}
          checked={!state.allEvents}
          onSelect={() => setState((c) => ({ ...c, allEvents: false }))}
          title="Only the events I choose"
          description={
            state.allEvents
              ? "Pick from the list."
              : `${state.events.size} selected`
          }
        />
      </div>

      {!state.allEvents && (
        <div
          className={cn(
            "max-h-80 overflow-y-auto rounded-lg border",
            error && "border-destructive",
          )}
        >
          {groups.map((group) => {
            const names = group.events.map((e) => e.name);
            const selected = names.filter((n) => state.events.has(n)).length;
            const all = selected === names.length;
            return (
              <div key={group.id} className="border-b last:border-b-0">
                <div className="sticky top-0 z-10 flex items-center justify-between gap-2 bg-muted/80 px-3 py-1.5 backdrop-blur-sm">
                  <span className="text-xs font-medium text-muted-foreground">
                    {group.title}
                    {selected > 0 ? (
                      <span className="ml-1.5 text-foreground/70">
                        {selected}/{names.length}
                      </span>
                    ) : null}
                  </span>
                  <button
                    type="button"
                    className="rounded-md px-1.5 py-0.5 text-xs font-medium text-muted-foreground transition-colors duration-150 ease-soft hover:bg-background hover:text-foreground"
                    onClick={() =>
                      setEvents((events) =>
                        names.forEach((n) => (all ? events.delete(n) : events.add(n))),
                      )
                    }
                  >
                    {all ? "Clear" : "Select all"}
                  </button>
                </div>
                <div className="grid gap-px p-1">
                  {group.events.map((e) => (
                    <label
                      key={e.name}
                      htmlFor={`${id}-evt-${e.name}`}
                      className="flex cursor-pointer items-start gap-3 rounded-md px-2 py-1.5 transition-colors duration-150 ease-soft hover:bg-muted/60"
                    >
                      <input
                        id={`${id}-evt-${e.name}`}
                        type="checkbox"
                        className="mt-0.5 size-4 shrink-0 accent-primary"
                        checked={state.events.has(e.name)}
                        onChange={(ev) =>
                          setEvents((events) =>
                            ev.target.checked
                              ? events.add(e.name)
                              : events.delete(e.name),
                          )
                        }
                      />
                      <span className="grid min-w-0 gap-0.5">
                        <span className="flex flex-wrap items-baseline gap-x-2 text-sm">
                          {e.title}
                          {e.title !== e.name ? (
                            <code className="font-mono text-[0.7rem] text-muted-foreground">
                              {e.name}
                            </code>
                          ) : null}
                        </span>
                        {e.description ? (
                          <span className="text-xs text-muted-foreground">
                            {e.description}
                          </span>
                        ) : null}
                      </span>
                    </label>
                  ))}
                </div>
              </div>
            );
          })}
        </div>
      )}
      {error ? (
        <p id={`${id}-events-error`} className="text-xs text-destructive">
          {error}
        </p>
      ) : null}
    </fieldset>
  );
}

function SecretField({
  id,
  mode,
  state,
  setState,
  error,
}: {
  id: string;
  mode: "create" | "edit";
  state: FormState;
  setState: Dispatch<SetStateAction<FormState>>;
  error?: string;
}) {
  const signatureNote = (
    <>
      Each delivery then carries an <code className="font-mono">X-Webhook-Hmac</code>{" "}
      header: the HMAC-SHA512 (hex) of the raw request body. Recompute it on your
      server to prove the request came from the gateway.{" "}
      <a
        href={`${DOCS.receiving}#before-you-trust-a-delivery`}
        target="_blank"
        rel="noreferrer"
        className="font-medium text-foreground/80 underline-offset-4 hover:text-foreground hover:underline"
      >
        How to verify
      </a>
    </>
  );

  if (mode === "edit" && !state.replaceSecret) {
    return (
      <div className="grid gap-1.5">
        <Label>Signing secret</Label>
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border bg-muted/30 px-3 py-2">
          <span className="text-sm text-muted-foreground">
            Hidden for security. If one is set, it keeps working.
          </span>
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => setState((c) => ({ ...c, replaceSecret: true }))}
          >
            Replace secret
          </Button>
        </div>
        <p className="text-xs text-pretty text-muted-foreground">{signatureNote}</p>
      </div>
    );
  }

  return (
    <Field
      label={mode === "edit" ? "New signing secret" : "Signing secret"}
      htmlFor={id}
      optional={mode === "create"}
      error={error}
      hint={
        <>
          {mode === "edit"
            ? "Saving replaces the old secret immediately; update your server at the same time. "
            : "Recommended. "}
          {signatureNote}
        </>
      }
    >
      <div className="flex items-center gap-2">
        <Input
          id={id}
          value={state.secret}
          onChange={(e) => setState((c) => ({ ...c, secret: e.target.value }))}
          placeholder="Paste a secret or generate one"
          autoComplete="off"
          spellCheck={false}
          className="font-mono text-xs"
          aria-invalid={Boolean(error) || undefined}
        />
        {state.secret ? (
          <CopyButton value={state.secret} label="Copy secret" iconOnly size="icon" />
        ) : null}
        <Button
          type="button"
          variant="outline"
          onClick={() => setState((c) => ({ ...c, secret: generateSecret() }))}
        >
          <SparklesIcon aria-hidden data-icon="inline-start" />
          Generate
        </Button>
        {mode === "edit" ? (
          <Button
            type="button"
            variant="ghost"
            onClick={() =>
              setState((c) => ({ ...c, replaceSecret: false, secret: "" }))
            }
          >
            Cancel
          </Button>
        ) : null}
      </div>
    </Field>
  );
}

function HeadersField({
  state,
  setState,
  error,
}: {
  state: FormState;
  setState: Dispatch<SetStateAction<FormState>>;
  error?: string;
}) {
  const setRow = (i: number, patch: Partial<HeaderRow>) =>
    setState((c) => ({
      ...c,
      headers: c.headers.map((row, j) => (j === i ? { ...row, ...patch } : row)),
    }));

  return (
    <fieldset className="grid gap-1.5">
      <legend className="mb-1.5 flex items-center gap-2 text-sm font-medium">
        Custom headers
        <span className="text-xs font-normal text-muted-foreground">optional</span>
      </legend>
      <p className="text-xs text-muted-foreground">
        Sent with every delivery, e.g. a static <code className="font-mono">Authorization</code>{" "}
        token your server expects.
      </p>
      {state.headers.map((h, i) => (
        <div key={i} className="flex items-center gap-2">
          <Input
            value={h.key}
            onChange={(e) => setRow(i, { key: e.target.value })}
            placeholder="Header name"
            aria-label="Header name"
            className="font-mono text-xs"
            aria-invalid={Boolean(error) || undefined}
          />
          <Input
            value={h.value}
            onChange={(e) => setRow(i, { value: e.target.value })}
            placeholder="Value"
            aria-label="Header value"
            className="font-mono text-xs"
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
            <XIcon aria-hidden />
          </Button>
        </div>
      ))}
      {error ? <p className="text-xs text-destructive">{error}</p> : null}
      <div>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() =>
            setState((c) => ({
              ...c,
              headers: [...c.headers, { key: "", value: "" }],
            }))
          }
        >
          <PlusIcon aria-hidden data-icon="inline-start" />
          Add header
        </Button>
      </div>
    </fieldset>
  );
}

function RetryField({
  id,
  mode,
  state,
  setState,
  errors,
}: {
  id: string;
  mode: "create" | "edit";
  state: FormState;
  setState: Dispatch<SetStateAction<FormState>>;
  errors: FieldErrors;
}) {
  const setRetry = (patch: Partial<RetryDraft>) =>
    setState((c) => ({ ...c, retry: { ...c.retry, ...patch } }));
  const last = lastRetryAfterSeconds(state.retry);

  return (
    <fieldset className="grid gap-2">
      <legend className="mb-1.5 text-sm font-medium">Retries</legend>
      <p className="text-xs text-pretty text-muted-foreground">
        If your server doesn't answer with a 2xx status, the gateway tries again.
        After the last attempt the delivery is dropped.
      </p>
      {mode === "create" ? (
        <label
          htmlFor={`${id}-custom`}
          className="flex cursor-pointer items-center gap-3 text-sm"
        >
          <input
            id={`${id}-custom`}
            type="checkbox"
            className="size-4 accent-primary"
            checked={state.customRetry}
            onChange={(e) =>
              setState((c) => ({ ...c, customRetry: e.target.checked }))
            }
          />
          Customize retries
          {!state.customRetry ? (
            <span className="text-xs text-muted-foreground">
              (using the gateway default)
            </span>
          ) : null}
        </label>
      ) : null}
      {state.customRetry ? (
        <div className="grid gap-3 rounded-lg border bg-muted/20 p-3">
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Attempts" htmlFor={`${id}-attempts`} error={errors.attempts}>
              <Input
                id={`${id}-attempts`}
                type="number"
                inputMode="numeric"
                min={1}
                max={MAX_ATTEMPTS}
                step={1}
                value={state.retry.attempts}
                onChange={(e) => setRetry({ attempts: e.target.value })}
                aria-invalid={Boolean(errors.attempts) || undefined}
              />
            </Field>
            <Field
              label="First wait (seconds)"
              htmlFor={`${id}-delay`}
              error={errors.delaySeconds}
            >
              <Input
                id={`${id}-delay`}
                type="number"
                inputMode="numeric"
                min={1}
                max={MAX_DELAY_SECONDS}
                step={1}
                value={state.retry.delaySeconds}
                onChange={(e) => setRetry({ delaySeconds: e.target.value })}
                aria-invalid={Boolean(errors.delaySeconds) || undefined}
              />
            </Field>
            <Field label="Backoff" htmlFor={`${id}-policy`}>
              <Select
                value={state.retry.policy}
                onValueChange={(v) => setRetry({ policy: v as RetryDraft["policy"] })}
              >
                <SelectTrigger id={`${id}-policy`} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent position="popper">
                  <SelectItem value="exponential">Doubling</SelectItem>
                  <SelectItem value="constant">Fixed</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
          {last !== null ? (
            <p className="text-xs text-muted-foreground">
              {Number(state.retry.attempts) <= 1
                ? "No retries: a failed delivery is dropped right away."
                : state.retry.policy === "exponential"
                  ? `Waits ${state.retry.delaySeconds}s, ${Number(state.retry.delaySeconds) * 2}s, ${Number(state.retry.delaySeconds) * 4}s… between attempts. The last retry happens about ${formatDuration(last)} after the first failure.`
                  : `Waits ${state.retry.delaySeconds}s between attempts. The last retry happens about ${formatDuration(last)} after the first failure.`}
            </p>
          ) : null}
        </div>
      ) : null}
    </fieldset>
  );
}

// --- small building blocks ------------------------------------------------

function Field({
  label,
  htmlFor,
  optional,
  hint,
  error,
  children,
}: {
  label: ReactNode;
  htmlFor?: string;
  optional?: boolean;
  hint?: ReactNode;
  error?: string;
  children: ReactNode;
}) {
  return (
    <div className="grid min-w-0 content-start gap-1.5">
      <div className="flex items-center gap-2">
        <Label htmlFor={htmlFor}>{label}</Label>
        {optional ? (
          <span className="text-xs text-muted-foreground">optional</span>
        ) : null}
      </div>
      {children}
      {error ? (
        <p className="text-xs text-destructive">{error}</p>
      ) : hint ? (
        <p className="text-xs text-pretty text-muted-foreground">{hint}</p>
      ) : null}
    </div>
  );
}

function ChoiceCard({
  name,
  checked,
  onSelect,
  title,
  description,
}: {
  name: string;
  checked: boolean;
  onSelect: () => void;
  title: string;
  description: string;
}) {
  return (
    <label
      className={cn(
        "flex cursor-pointer items-start gap-2.5 rounded-lg border px-3 py-2.5 transition-colors duration-150 ease-soft hover:bg-muted/40",
        checked && "border-ring/60 bg-muted/50 hover:bg-muted/50",
      )}
    >
      <input
        type="radio"
        name={name}
        className="mt-0.5 size-4 shrink-0 accent-primary"
        checked={checked}
        onChange={onSelect}
      />
      <span className="grid gap-0.5">
        <span className="text-sm font-medium">{title}</span>
        <span className="text-xs text-muted-foreground">{description}</span>
      </span>
    </label>
  );
}

/** A controlled show/hide section with a chevron header. */
function Disclosure({
  open,
  onOpenChange,
  title,
  summary,
  attention,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  summary: string;
  /** Highlight the header (e.g. a validation error inside). */
  attention?: boolean;
  children: ReactNode;
}) {
  const id = useId();
  return (
    <div className={cn("rounded-lg border", attention && "border-destructive")}>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => onOpenChange(!open)}
        className="flex w-full items-center gap-2 rounded-lg px-3 py-2.5 text-left transition-colors duration-150 ease-soft hover:bg-muted/40"
      >
        <ChevronRightIcon
          aria-hidden
          className={cn(
            "size-4 shrink-0 text-muted-foreground transition-transform duration-200 ease-soft",
            open && "rotate-90",
          )}
        />
        <span className="text-sm font-medium">{title}</span>
        <span
          className={cn(
            "truncate text-xs",
            attention ? "text-destructive" : "text-muted-foreground",
          )}
        >
          {attention ? "Check the fields below" : summary}
        </span>
      </button>
      {open ? (
        <div id={id} className="grid gap-5 border-t px-3 pt-4 pb-3">
          {children}
        </div>
      ) : null}
    </div>
  );
}
