// The create/edit form for a "Sign in with WhatsApp" app (oauth.md §6.2). Drives
// both the "New app" page and the detail-page Settings tab from one controlled
// state object. The form is a stack of section cards (app details, WhatsApp
// number, redirect URIs, shared info, advanced); the right column carries a
// LIVE preview of the end-user consent card (reusing the real ConsentCard).
// All API shapes come from the typed hooks — the parent owns the mutation; this
// owns the form.

import * as React from "react";
import { Link } from "@tanstack/react-router";
import {
  AlertCircleIcon,
  ArrowRightIcon,
  CheckCircle2Icon,
  ChevronRightIcon,
  PlusIcon,
  SmartphoneIcon,
  XIcon,
} from "lucide-react";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "~/components/ui/select";
import { sessionStatusLabel } from "~/components/status-badge";
import { cn } from "~/lib/utils";
import { useSessions } from "~/lib/api/hooks/sessions";
import { useGroups } from "~/lib/api/hooks/groups";
import type {
  OAuthApp,
  OAuthAppBody,
  OAuthMode,
  OAuthClientType,
} from "~/lib/api/hooks/oauth";
import { ConsentPreview } from "./ConsentPreview";
import { DASH_SCOPES } from "./scopes";
import { buildPreviewSnapshot } from "./preview";
import { MODE_META } from "./ui";
import {
  validateLoginCommand,
  validateRedirectUris,
  normalizeRedirectUris,
  clamp,
  humanizeSeconds,
  TOKEN_TTL,
  REFRESH_TTL,
} from "./validation";
import { sessionTitle } from "~/lib/format";

export interface OAuthFormState {
  name: string;
  botName: string;
  logoUrl: string;
  loginCommand: string;
  sessionId: string;
  redirectUris: string[];
  modes: OAuthMode[];
  groupJid: string;
  scopes: string[];
  clientType: OAuthClientType;
  tokenTtlSeconds: number;
  refreshTtlSeconds: number;
}

export function emptyFormState(): OAuthFormState {
  return {
    name: "",
    botName: "",
    logoUrl: "",
    loginCommand: "login",
    sessionId: "",
    redirectUris: [""],
    modes: ["dm"],
    groupJid: "",
    scopes: ["openid", "profile"],
    clientType: "confidential",
    tokenTtlSeconds: TOKEN_TTL.default,
    refreshTtlSeconds: REFRESH_TTL.default,
  };
}

export function formStateFromApp(app: OAuthApp): OAuthFormState {
  return {
    name: app.name,
    botName: app.botName ?? "",
    logoUrl: app.logoUrl ?? "",
    loginCommand: app.loginCommand,
    sessionId: app.sessionId,
    redirectUris: app.redirectUris?.length ? app.redirectUris : [""],
    modes: (app.modes ?? ["dm"]) as OAuthMode[],
    groupJid: app.groupJid ?? "",
    scopes: app.allowedScopes ?? ["openid"],
    clientType: app.clientType,
    tokenTtlSeconds: app.tokenTtlSeconds,
    refreshTtlSeconds: app.refreshTtlSeconds,
  };
}

/** Turn the form state into the API request body, normalizing as it goes. */
export function toRequestBody(s: OAuthFormState): OAuthAppBody {
  const scopes = s.scopes.includes("openid")
    ? s.scopes
    : ["openid", ...s.scopes];
  return {
    name: s.name.trim(),
    botName: s.botName.trim() || undefined,
    logoUrl: s.logoUrl.trim() || undefined,
    loginCommand: s.loginCommand.trim(),
    sessionId: s.sessionId,
    redirectUris: normalizeRedirectUris(s.redirectUris),
    modes: s.modes,
    groupJid: s.modes.includes("group") ? s.groupJid.trim() || undefined : undefined,
    allowedScopes: scopes,
    clientType: s.clientType,
    tokenTtlSeconds: s.tokenTtlSeconds,
    refreshTtlSeconds: s.refreshTtlSeconds,
  };
}

/** Comparable snapshot of the form, for "unsaved changes" checks. */
export function formFingerprint(s: OAuthFormState): string {
  return JSON.stringify(toRequestBody(s));
}

export interface FormErrors {
  name?: string;
  sessionId?: string;
  modes?: string;
  groupJid?: string;
  loginCommand?: string;
  redirect?: string;
}

/** Field order on screen; the first failing one gets scrolled to on submit. */
const ERROR_ORDER: (keyof FormErrors)[] = [
  "name",
  "sessionId",
  "modes",
  "groupJid",
  "loginCommand",
  "redirect",
];

const ERROR_LABELS: Record<keyof FormErrors, string> = {
  name: "app name",
  sessionId: "WhatsApp number",
  modes: "verification method",
  groupJid: "sign-in group",
  loginCommand: "login keyword",
  redirect: "redirect URI",
};

export function validateForm(s: OAuthFormState): FormErrors {
  const errors: FormErrors = {};
  if (!s.name.trim()) errors.name = "Give your app a name.";
  if (!s.sessionId) errors.sessionId = "Choose the WhatsApp number people will message.";
  if (s.modes.length === 0) errors.modes = "Turn on at least one verification method.";
  if (s.modes.includes("group") && !s.groupJid.trim()) {
    errors.groupJid = "Choose the group users must be in.";
  }
  const cmd = validateLoginCommand(s.loginCommand);
  if (cmd) errors.loginCommand = cmd;
  const uris = validateRedirectUris(s.redirectUris);
  if (!uris.ok) {
    errors.redirect = s.redirectUris.some((u) => u.trim())
      ? "Fix the redirect URIs above."
      : "Add at least one redirect URI.";
  }
  return errors;
}

export function isFormValid(s: OAuthFormState): boolean {
  return Object.keys(validateForm(s)).length === 0;
}

/** Plain-language list of what's missing, e.g. "app name, WhatsApp number". */
export function describeErrors(errors: FormErrors): string {
  return ERROR_ORDER.filter((k) => errors[k])
    .map((k) => ERROR_LABELS[k])
    .join(", ");
}

/** Scroll to (and focus) the first invalid field rendered by <OAuthAppForm>. */
export function focusFirstError(idPrefix: string, errors: FormErrors): void {
  const key = ERROR_ORDER.find((k) => errors[k]);
  if (!key || typeof document === "undefined") return;
  const el = document.getElementById(`${idPrefix}-field-${key}`);
  if (!el) return;
  el.scrollIntoView({ behavior: "smooth", block: "center" });
  const focusable = el.querySelector<HTMLElement>(
    "input:not([disabled]), button[role='combobox'], button:not([disabled])",
  );
  focusable?.focus({ preventScroll: true });
}

export function OAuthAppForm({
  state,
  onChange,
  idPrefix = "oauth",
  showPreview = true,
  showErrors = false,
  autoFocus = false,
}: {
  state: OAuthFormState;
  onChange: (next: OAuthFormState) => void;
  idPrefix?: string;
  showPreview?: boolean;
  /** Show every validation error (set after a submit attempt). */
  showErrors?: boolean;
  autoFocus?: boolean;
}) {
  const set = <K extends keyof OAuthFormState>(
    key: K,
    value: OAuthFormState[K],
  ) => onChange({ ...state, [key]: value });

  // Redirect rows the user has left (blurred): their errors show from then on.
  const [touchedUris, setTouchedUris] = React.useState<Set<number>>(
    () => new Set(),
  );

  const sessions = useSessions();
  const sessionRows = sessions.data?.pages.flatMap((p) => p.data) ?? [];
  const workingCount = sessionRows.filter((s) => s.status === "working").length;
  const boundSession = sessionRows.find((s) => s.id === state.sessionId);

  const groups = useGroups(state.modes.includes("group") ? state.sessionId : "");
  const groupRows = groups.data?.pages.flatMap((p) => p.data) ?? [];
  const selectedGroup = groupRows.find((g) => g.groupJid === state.groupJid);

  const errors = validateForm(state);
  const fieldId = (key: keyof FormErrors) => `${idPrefix}-field-${key}`;
  const shown = (key: keyof FormErrors) => (showErrors ? errors[key] : undefined);
  // The login keyword validates as you type (it has a sensible default).
  const commandError = state.loginCommand ? errors.loginCommand : shown("loginCommand");
  const uriIssues = validateRedirectUris(state.redirectUris);

  const previewSnapshot = buildPreviewSnapshot({
    name: state.name,
    botName: state.botName,
    logoUrl: state.logoUrl,
    loginCommand: state.loginCommand,
    scopes: state.scopes,
    modes: state.modes,
    botNumber: boundSession?.phoneNumber
      ? `+${boundSession.phoneNumber}`
      : undefined,
    groupName: selectedGroup?.subject,
  });

  const toggleMode = (mode: OAuthMode) => {
    const has = state.modes.includes(mode);
    set("modes", has ? state.modes.filter((m) => m !== mode) : [...state.modes, mode]);
  };

  const toggleScope = (key: string) => {
    if (key === "openid") return; // required
    const has = state.scopes.includes(key);
    set("scopes", has ? state.scopes.filter((s) => s !== key) : [...state.scopes, key]);
  };

  const setUri = (i: number, value: string) => {
    const next = [...state.redirectUris];
    next[i] = value;
    set("redirectUris", next);
  };
  const addUri = () => set("redirectUris", [...state.redirectUris, ""]);
  const removeUri = (i: number) => {
    setTouchedUris(new Set());
    set(
      "redirectUris",
      state.redirectUris.length > 1
        ? state.redirectUris.filter((_, idx) => idx !== i)
        : [""],
    );
  };

  const loginWord = state.loginCommand.trim() || "login";

  return (
    <div
      className={cn(
        "grid items-start gap-6",
        showPreview && "lg:grid-cols-[minmax(0,1fr)_minmax(0,22rem)] xl:gap-8",
      )}
    >
      <div className="grid min-w-0 gap-6">
        {/* 1. What people see */}
        <Section
          title="App details"
          description="How your app appears on the sign-in page and in the WhatsApp reply."
        >
          <Field
            id={fieldId("name")}
            label="App name"
            htmlFor={`${idPrefix}-name`}
            error={shown("name")}
          >
            <Input
              id={`${idPrefix}-name`}
              value={state.name}
              onChange={(e) => set("name", e.target.value)}
              placeholder="e.g. Acme Support Portal"
              aria-invalid={Boolean(shown("name"))}
              autoFocus={autoFocus}
            />
            <Hint>Users see this name when they sign in, so they know who is asking.</Hint>
          </Field>

          <div className="grid gap-5 sm:grid-cols-2">
            <Field label="Logo URL" htmlFor={`${idPrefix}-logo`} optional>
              <Input
                id={`${idPrefix}-logo`}
                value={state.logoUrl}
                onChange={(e) => set("logoUrl", e.target.value)}
                placeholder="https://acme.com/logo.png"
                inputMode="url"
              />
              <Hint>A square image works best.</Hint>
            </Field>
            <Field label="Bot name" htmlFor={`${idPrefix}-bot-name`} optional>
              <Input
                id={`${idPrefix}-bot-name`}
                value={state.botName}
                onChange={(e) => set("botName", e.target.value)}
                placeholder="e.g. Acme Support"
                maxLength={255}
              />
              <Hint>The WhatsApp profile name of your number, so users recognize it.</Hint>
            </Field>
          </div>
        </Section>

        {/* 2. The number + how users prove themselves */}
        <Section
          title="WhatsApp number"
          description="People sign in by sending a short code from their WhatsApp to this number."
        >
          <Field
            id={fieldId("sessionId")}
            label="Number people message"
            htmlFor={`${idPrefix}-session`}
            error={shown("sessionId")}
          >
            {!sessions.isLoading && workingCount === 0 && !boundSession ? (
              <NoConnectedNumber hasSessions={sessionRows.length > 0} />
            ) : (
              <>
                <Select
                  value={state.sessionId}
                  onValueChange={(v) => set("sessionId", v)}
                >
                  <SelectTrigger
                    id={`${idPrefix}-session`}
                    className="w-full"
                    aria-invalid={Boolean(shown("sessionId"))}
                  >
                    <SelectValue
                      placeholder={sessions.isLoading ? "Loading numbers…" : "Choose a number…"}
                    />
                  </SelectTrigger>
                  <SelectContent>
                    {sessionRows.map((s) => (
                      <SelectItem
                        key={s.id}
                        value={s.id}
                        disabled={s.status !== "working" && s.id !== state.sessionId}
                      >
                        {sessionTitle(s)}
                        {s.phoneNumber ? ` · +${s.phoneNumber}` : ""}
                        {s.status !== "working" ? ` (${sessionStatusLabel(s.status)})` : ""}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {boundSession && boundSession.status !== "working" ? (
                  <Callout tone="warn">
                    This number is {sessionStatusLabel(boundSession.status).toLowerCase()}.
                    Sign-ins fail until it is connected again.
                  </Callout>
                ) : (
                  <Hint>
                    Only connected numbers can be chosen. Need another one?{" "}
                    <InlineLink to="/user/sessions">Connect a number</InlineLink>
                  </Hint>
                )}
              </>
            )}
          </Field>

          <Field
            id={fieldId("modes")}
            label="How users verify"
            error={shown("modes")}
          >
            <div className="grid gap-2 sm:grid-cols-2">
              {(["dm", "group"] as const).map((m) => (
                <ModeToggle
                  key={m}
                  active={state.modes.includes(m)}
                  onClick={() => toggleMode(m)}
                  title={MODE_META[m].label}
                  subtitle={MODE_META[m].description}
                />
              ))}
            </div>
            {state.modes.length === 2 ? (
              <Hint>
                Users get direct message by default. Your app can ask for group
                verification per sign-in.
              </Hint>
            ) : null}
          </Field>

          {state.modes.includes("group") && (
            <Field
              id={fieldId("groupJid")}
              label="Group users must be in"
              htmlFor={`${idPrefix}-group`}
              error={shown("groupJid")}
            >
              {groupRows.length > 0 ? (
                <Select
                  value={state.groupJid}
                  onValueChange={(v) => set("groupJid", v)}
                >
                  <SelectTrigger
                    id={`${idPrefix}-group`}
                    className="w-full"
                    aria-invalid={Boolean(shown("groupJid"))}
                  >
                    <SelectValue placeholder="Choose a group…">
                      {state.groupJid
                        ? selectedGroup?.subject || state.groupJid
                        : undefined}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {groupRows.map((g) => (
                      <SelectItem key={g.groupJid} value={g.groupJid}>
                        <span className="flex min-w-0 flex-col items-start">
                          <span className="truncate">
                            {g.subject || g.groupJid}
                            {g.participants ? ` · ${g.participants} members` : ""}
                          </span>
                          {g.subject ? (
                            <span className="truncate font-mono text-xs text-muted-foreground">
                              {g.groupJid}
                            </span>
                          ) : null}
                        </span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : (
                <Input
                  id={`${idPrefix}-group`}
                  value={state.groupJid}
                  onChange={(e) => set("groupJid", e.target.value)}
                  placeholder="120363025000000000@g.us"
                  className="font-mono"
                  aria-invalid={Boolean(shown("groupJid"))}
                  spellCheck={false}
                />
              )}
              <Hint>
                {!state.sessionId
                  ? "Choose the number first to list the groups it is in."
                  : groups.isLoading
                    ? "Loading this number's groups…"
                    : groupRows.length === 0
                      ? "No groups found on this number yet. Paste the group ID instead; it ends in @g.us."
                      : "Users post the code in this group to prove they are members. Your number must be in the group."}
              </Hint>
            </Field>
          )}

          <Field
            id={fieldId("loginCommand")}
            label="Login keyword"
            htmlFor={`${idPrefix}-cmd`}
            error={commandError}
          >
            <Input
              id={`${idPrefix}-cmd`}
              value={state.loginCommand}
              onChange={(e) => set("loginCommand", e.target.value.toLowerCase())}
              className="font-mono sm:w-56"
              placeholder="login"
              spellCheck={false}
              autoCapitalize="off"
              aria-invalid={Boolean(commandError)}
            />
            <Hint>
              The word users type before the 6-digit code. Example message:{" "}
              <code className="rounded-sm bg-muted px-1 py-0.5 font-mono text-foreground">
                {loginWord} 483920
              </code>
            </Hint>
            <Callout tone="warn">
              Messages to this number that start with{" "}
              <code className="font-mono text-foreground">{loginWord}</code> are used for
              sign-in and{" "}
              <span className="font-medium text-foreground">don't reach your webhooks</span>.
            </Callout>
          </Field>
        </Section>

        {/* 3. Redirect URIs */}
        <Section
          title="Redirect URIs"
          description="After someone signs in, they're sent back to one of these addresses on your site, along with a one-time code your app exchanges for their details."
        >
          <Field
            id={fieldId("redirect")}
            label="Allowed redirect URIs"
            error={shown("redirect")}
          >
            <div className="grid gap-2">
              {state.redirectUris.map((uri, i) => {
                const visible = showErrors || touchedUris.has(i);
                const err = uri.trim() && visible ? uriIssues.perUri[i] : null;
                const dup = visible && uriIssues.duplicates.has(i);
                return (
                  <div key={i} className="grid gap-1">
                    <div className="flex items-center gap-2">
                      <Input
                        value={uri}
                        onChange={(e) => setUri(i, e.target.value)}
                        onBlur={() =>
                          setTouchedUris((cur) => new Set(cur).add(i))
                        }
                        placeholder="https://app.example.com/auth/callback"
                        aria-label={`Redirect URI ${i + 1}`}
                        aria-invalid={Boolean(err || dup)}
                        className="font-mono"
                        inputMode="url"
                        spellCheck={false}
                      />
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="shrink-0 text-muted-foreground"
                        onClick={() => removeUri(i)}
                        aria-label="Remove redirect URI"
                        disabled={state.redirectUris.length === 1 && !uri}
                      >
                        <XIcon aria-hidden />
                      </Button>
                    </div>
                    {err || dup ? (
                      <p className="text-xs text-destructive">
                        {err ?? "This URI is already in the list."}
                      </p>
                    ) : null}
                  </div>
                );
              })}
            </div>
            <div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={addUri}
              >
                <PlusIcon aria-hidden data-icon="inline-start" />
                Add another
              </Button>
            </div>
            <Hint>
              Must match exactly what your app sends (no wildcards). Use{" "}
              <code className="font-mono">https://</code>;{" "}
              <code className="font-mono">http://localhost</code> is allowed while
              you develop.
            </Hint>
          </Field>
        </Section>

        {/* 4. Scopes */}
        <Section
          title="What your app receives"
          description="Choose which details about the user your app gets after they sign in (OAuth scopes). Users see this list before they confirm."
        >
          <div className="-mx-2 grid gap-0.5">
            {DASH_SCOPES.map((scope) => {
              const checked = scope.required || state.scopes.includes(scope.key);
              const inputId = `${idPrefix}-scope-${scope.key.replace(/[^a-z_]/gi, "-")}`;
              return (
                <label
                  key={scope.key}
                  htmlFor={inputId}
                  className={cn(
                    "flex items-start gap-3 rounded-lg px-2 py-2 text-sm transition-colors duration-150 ease-soft",
                    scope.required ? "cursor-default" : "cursor-pointer hover:bg-muted/60",
                  )}
                >
                  <input
                    id={inputId}
                    type="checkbox"
                    className="mt-0.5 size-4 shrink-0 accent-primary"
                    checked={checked}
                    disabled={scope.required}
                    onChange={() => toggleScope(scope.key)}
                  />
                  <span className="grid min-w-0 gap-0.5">
                    <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                      <span className="font-medium">{scope.title}</span>
                      <code className="font-mono text-xs text-muted-foreground">
                        {scope.label}
                      </code>
                      {scope.required ? (
                        <span className="text-xs text-muted-foreground">· always included</span>
                      ) : null}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      {scope.description}
                    </span>
                  </span>
                </label>
              );
            })}
          </div>
        </Section>

        {/* 5. Advanced */}
        <details className="group/adv overflow-hidden rounded-xl border bg-card shadow-[0_1px_2px_rgb(0_0_0/0.04)] dark:shadow-none">
          <summary className="flex cursor-pointer list-none items-center gap-2 px-5 py-4 select-none [&::-webkit-details-marker]:hidden">
            <ChevronRightIcon
              className="size-4 text-muted-foreground transition-transform duration-200 ease-soft group-open/adv:rotate-90"
              aria-hidden
            />
            <span className="font-semibold tracking-tight">Advanced</span>
            <span className="truncate text-sm text-muted-foreground">
              App type and how long sign-ins last
            </span>
          </summary>
          <div className="grid gap-5 border-t px-5 py-5">
            <Field label="App type" htmlFor={`${idPrefix}-type`}>
              <Select
                value={state.clientType}
                onValueChange={(v) => set("clientType", v as OAuthClientType)}
              >
                <SelectTrigger id={`${idPrefix}-type`} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="confidential">
                    Server app (confidential client, has a secret)
                  </SelectItem>
                  <SelectItem value="public">
                    Browser or mobile app (public client, no secret)
                  </SelectItem>
                </SelectContent>
              </Select>
              <Hint>
                {state.clientType === "public"
                  ? "For apps that run on the user's device and can't keep a secret. They prove themselves with PKCE alone."
                  : "Pick this if your app has a backend. You get a client secret once, after creating the app. PKCE is still required."}
              </Hint>
            </Field>

            <div className="grid gap-5 sm:grid-cols-2">
              <TtlField
                label="Access token lifetime"
                hint="How long an access / ID token stays valid."
                id={`${idPrefix}-ttl-token`}
                value={state.tokenTtlSeconds}
                min={TOKEN_TTL.min}
                max={TOKEN_TTL.max}
                presets={[300, 900, 3_600, 86_400]}
                onChange={(v) => set("tokenTtlSeconds", v)}
              />
              <TtlField
                label="Stay signed in for"
                hint="Refresh token lifetime (needs “Stay signed in”)."
                id={`${idPrefix}-ttl-refresh`}
                value={state.refreshTtlSeconds}
                min={REFRESH_TTL.min}
                max={REFRESH_TTL.max}
                presets={[86_400, 604_800, 2_592_000, 7_776_000]}
                onChange={(v) => set("refreshTtlSeconds", v)}
              />
            </div>
          </div>
        </details>
      </div>

      {/* Right: live consent preview */}
      {showPreview && (
        <div className="min-w-0 lg:sticky lg:top-4 lg:self-start">
          <ConsentPreview snapshot={previewSnapshot} />
        </div>
      )}
    </div>
  );
}

// --- Small building blocks --------------------------------------------------

function Section({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: React.ReactNode;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        <CardDescription className="text-pretty">{description}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-5">{children}</CardContent>
    </Card>
  );
}

function Field({
  id,
  label,
  htmlFor,
  optional,
  error,
  children,
}: {
  id?: string;
  label: string;
  htmlFor?: string;
  optional?: boolean;
  error?: string | null;
  children: React.ReactNode;
}) {
  return (
    <div id={id} className="grid scroll-mt-24 content-start gap-2">
      <div className="flex items-center gap-2">
        <Label htmlFor={htmlFor}>{label}</Label>
        {optional && <span className="text-xs text-muted-foreground">Optional</span>}
      </div>
      {children}
      {error ? (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  );
}

function Hint({ children }: { children: React.ReactNode }) {
  return <p className="text-xs text-pretty text-muted-foreground">{children}</p>;
}

function Callout({ tone, children }: { tone: "warn"; children: React.ReactNode }) {
  return (
    <p
      className={cn(
        "flex items-start gap-2 rounded-lg px-3 py-2 text-xs text-pretty",
        tone === "warn" && "bg-amber-500/10 text-amber-900 dark:text-amber-200",
      )}
    >
      <AlertCircleIcon
        className="mt-px size-3.5 shrink-0 text-amber-600 dark:text-amber-400"
        aria-hidden
      />
      <span>{children}</span>
    </p>
  );
}

function InlineLink({ to, children }: { to: "/user/sessions"; children: React.ReactNode }) {
  return (
    <Link
      to={to}
      className="font-medium text-foreground underline-offset-4 hover:underline"
    >
      {children}
    </Link>
  );
}

/** Shown instead of the picker when there's no connected number to bind. */
function NoConnectedNumber({ hasSessions }: { hasSessions: boolean }) {
  return (
    <div className="flex flex-col gap-3 rounded-lg bg-muted/60 p-3 sm:flex-row sm:items-center sm:justify-between">
      <div className="flex items-start gap-3">
        <SmartphoneIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
        <p className="text-sm text-pretty text-muted-foreground">
          <span className="font-medium text-foreground">
            {hasSessions ? "None of your numbers are connected." : "Connect a WhatsApp number first."}
          </span>{" "}
          Sign-in codes are sent to a connected number, so you need one before
          this app can work.
        </p>
      </div>
      <Button asChild size="sm" variant="outline" className="shrink-0">
        <Link to="/user/sessions">
          {hasSessions ? "Open sessions" : "Connect a number"}
          <ArrowRightIcon aria-hidden data-icon="inline-end" />
        </Link>
      </Button>
    </div>
  );
}

function ModeToggle({
  active,
  onClick,
  title,
  subtitle,
}: {
  active: boolean;
  onClick: () => void;
  title: string;
  subtitle: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "flex flex-col items-start gap-1 rounded-lg border p-3 text-left transition-[border-color,background-color,box-shadow] duration-150 ease-soft outline-none focus-visible:ring-3 focus-visible:ring-ring/30",
        active
          ? "border-foreground/25 bg-muted/50 shadow-xs"
          : "hover:border-foreground/20 hover:bg-muted/30",
      )}
    >
      <span className="flex w-full items-center justify-between gap-2">
        <span className="text-sm font-medium">{title}</span>
        {active ? (
          <CheckCircle2Icon className="size-4 text-emerald-600 dark:text-emerald-400" aria-hidden />
        ) : (
          <span className="size-4 rounded-full border border-foreground/20" aria-hidden />
        )}
      </span>
      <span className="text-xs text-pretty text-muted-foreground">{subtitle}</span>
    </button>
  );
}

const CUSTOM = "custom";

/** Lifetime picker: common presets, plus "Custom" for an exact second count. */
function TtlField({
  label,
  hint,
  id,
  value,
  min,
  max,
  presets,
  onChange,
}: {
  label: string;
  hint: string;
  id: string;
  value: number;
  min: number;
  max: number;
  presets: number[];
  onChange: (v: number) => void;
}) {
  const [custom, setCustom] = React.useState(() => !presets.includes(value));
  const selectValue = custom ? CUSTOM : String(value);
  return (
    <div className="grid content-start gap-2">
      <Label htmlFor={id}>{label}</Label>
      <Select
        value={selectValue}
        onValueChange={(v) => {
          if (v === CUSTOM) {
            setCustom(true);
            return;
          }
          setCustom(false);
          onChange(Number(v));
        }}
      >
        <SelectTrigger id={id} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {presets.map((p) => (
            <SelectItem key={p} value={String(p)}>
              {humanizeSeconds(p)}
            </SelectItem>
          ))}
          <SelectItem value={CUSTOM}>Custom…</SelectItem>
        </SelectContent>
      </Select>
      {custom ? (
        <div className="flex items-center gap-2">
          <Input
            type="number"
            aria-label={`${label} in seconds`}
            value={value}
            min={min}
            max={max}
            onChange={(e) => onChange(Number(e.target.value))}
            onBlur={(e) => onChange(clamp(Number(e.target.value), min, max))}
            className="w-32"
          />
          <span className="text-sm text-muted-foreground">
            seconds · {humanizeSeconds(clamp(value, min, max))}
          </span>
        </div>
      ) : null}
      <Hint>
        {hint}
        {custom ? ` Between ${humanizeSeconds(min)} and ${humanizeSeconds(max)}.` : ""}
      </Hint>
    </div>
  );
}
