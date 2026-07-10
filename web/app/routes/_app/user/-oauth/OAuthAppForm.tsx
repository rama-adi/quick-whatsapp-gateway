// The create/edit form for an OAuth application (oauth.md §6.2). Drives both the
// "New app" page and the detail-page editor from one controlled state object.
// Right column carries a LIVE preview of the end-user consent card (reusing the
// real ConsentCard) plus the interception note. All API shapes come from the
// typed hooks — the parent owns the mutation; this owns the form.

import * as React from "react";
import {
  PlusIcon,
  TrashIcon,
  AlertCircleIcon,
  CheckCircle2Icon,
} from "lucide-react";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Badge } from "~/components/ui/badge";
import { Separator } from "~/components/ui/separator";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "~/components/ui/select";
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
import {
  validateLoginCommand,
  validateRedirectUris,
  normalizeRedirectUris,
  clamp,
  humanizeSeconds,
  TOKEN_TTL,
  REFRESH_TTL,
} from "./validation";

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

export interface FormErrors {
  name?: string;
  loginCommand?: string | null;
  sessionId?: string;
  redirect?: boolean;
  groupJid?: string;
}

export function validateForm(s: OAuthFormState): FormErrors {
  const errors: FormErrors = {};
  if (!s.name.trim()) errors.name = "Give your app a name.";
  errors.loginCommand = validateLoginCommand(s.loginCommand);
  if (!s.sessionId) errors.sessionId = "Pick a bound session.";
  const uris = validateRedirectUris(s.redirectUris);
  if (!uris.ok) errors.redirect = true;
  if (s.modes.includes("group") && !s.groupJid.trim()) {
    errors.groupJid = "Select the pinned group.";
  }
  return errors;
}

export function isFormValid(s: OAuthFormState): boolean {
  const e = validateForm(s);
  return (
    !e.name &&
    !e.loginCommand &&
    !e.sessionId &&
    !e.redirect &&
    !e.groupJid &&
    s.modes.length > 0
  );
}

export function OAuthAppForm({
  state,
  onChange,
  idPrefix = "oauth",
  showPreview = true,
}: {
  state: OAuthFormState;
  onChange: (next: OAuthFormState) => void;
  idPrefix?: string;
  showPreview?: boolean;
}) {
  const set = <K extends keyof OAuthFormState>(
    key: K,
    value: OAuthFormState[K],
  ) => onChange({ ...state, [key]: value });

  const sessions = useSessions();
  const sessionRows = sessions.data?.pages.flatMap((p) => p.data) ?? [];
  // "Working" sessions are the only usable bots; still show others disabled so
  // the picker isn't mysteriously empty.
  const boundSession = sessionRows.find((s) => s.id === state.sessionId);

  const groups = useGroups(state.modes.includes("group") ? state.sessionId : "");
  const groupRows = groups.data?.pages.flatMap((p) => p.data) ?? [];
  const selectedGroup = groupRows.find((g) => g.groupJid === state.groupJid);

  const commandError = validateLoginCommand(state.loginCommand);
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
    const next = has
      ? state.modes.filter((m) => m !== mode)
      : [...state.modes, mode];
    set("modes", next);
  };

  const toggleScope = (key: string) => {
    if (key === "openid") return; // required
    const has = state.scopes.includes(key);
    set(
      "scopes",
      has ? state.scopes.filter((s) => s !== key) : [...state.scopes, key],
    );
  };

  const setUri = (i: number, value: string) => {
    const next = [...state.redirectUris];
    next[i] = value;
    set("redirectUris", next);
  };
  const addUri = () => set("redirectUris", [...state.redirectUris, ""]);
  const removeUri = (i: number) =>
    set(
      "redirectUris",
      state.redirectUris.length > 1
        ? state.redirectUris.filter((_, idx) => idx !== i)
        : [""],
    );

  return (
    <div className={cn("grid gap-8", showPreview && "lg:grid-cols-[minmax(0,1fr)_minmax(0,22rem)]")}>
      {/* Left: the form */}
      <div className="space-y-6">
        {/* Identity */}
        <Field label="Name" htmlFor={`${idPrefix}-name`}>
          <Input
            id={`${idPrefix}-name`}
            value={state.name}
            onChange={(e) => set("name", e.target.value)}
            placeholder="e.g. Acme Support Portal"
            autoFocus
          />
          <Hint>Shown to end-users on the sign-in screen and the bot's reply.</Hint>
        </Field>

        <Field label="Bot name" htmlFor={`${idPrefix}-bot-name`} optional>
          <Input
            id={`${idPrefix}-bot-name`}
            value={state.botName}
            onChange={(e) => set("botName", e.target.value)}
            placeholder="e.g. Acme Support"
            maxLength={255}
          />
          <Hint>
            Set this to the WhatsApp account's display name so users recognize
            the bot.
          </Hint>
        </Field>

        <Field label="Logo URL" htmlFor={`${idPrefix}-logo`} optional>
          <Input
            id={`${idPrefix}-logo`}
            value={state.logoUrl}
            onChange={(e) => set("logoUrl", e.target.value)}
            placeholder="https://acme.com/logo.png"
            inputMode="url"
          />
        </Field>

        {/* Login command */}
        <Field
          label="Login command"
          htmlFor={`${idPrefix}-cmd`}
          error={state.loginCommand ? commandError : undefined}
        >
          <div className="flex items-center gap-2">
            <div className="flex items-center rounded-md border bg-muted/40 font-mono text-sm">
              <Input
                id={`${idPrefix}-cmd`}
                value={state.loginCommand}
                onChange={(e) =>
                  set("loginCommand", e.target.value.toLowerCase())
                }
                className="w-40 border-0 bg-transparent font-mono shadow-none focus-visible:ring-0"
                placeholder="login"
                spellCheck={false}
              />
              <span className="px-2 text-muted-foreground">483920</span>
            </div>
          </div>
          <Hint>
            The keyword end-users type before the 6-digit code (e.g.{" "}
            <code className="font-mono">masuk</code> for an Indonesian app).
          </Hint>
          <div className="mt-2 flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/5 p-2.5 text-xs text-muted-foreground">
            <AlertCircleIcon
              className="mt-0.5 size-3.5 shrink-0 text-amber-600 dark:text-amber-500"
              aria-hidden
            />
            <span>
              Messages starting with{" "}
              <code className="font-mono text-foreground">
                {state.loginCommand.trim() || "login"}
              </code>{" "}
              on the bound session are intercepted for sign-in and{" "}
              <span className="font-medium text-foreground">
                won't reach your webhooks
              </span>
              .
            </span>
          </div>
        </Field>

        <Separator />

        {/* Bound session */}
        <Field
          label="Bound session"
          htmlFor={`${idPrefix}-session`}
          error={undefined}
        >
          <Select
            value={state.sessionId}
            onValueChange={(v) => set("sessionId", v)}
          >
            <SelectTrigger id={`${idPrefix}-session`} className="w-full">
              <SelectValue placeholder="Choose the bot session…" />
            </SelectTrigger>
            <SelectContent>
              {sessionRows.length === 0 ? (
                <div className="px-2 py-3 text-sm text-muted-foreground">
                  No sessions yet — create one first.
                </div>
              ) : (
                sessionRows.map((s) => (
                  <SelectItem
                    key={s.id}
                    value={s.id}
                    disabled={s.status !== "working"}
                  >
                    {s.label || s.id}
                    {s.phoneNumber ? ` · +${s.phoneNumber}` : ""}
                    {s.status !== "working" ? ` (${s.status.replace(/_/g, " ")})` : ""}
                  </SelectItem>
                ))
              )}
            </SelectContent>
          </Select>
          <Hint>
            The WhatsApp number that acts as the sign-in bot. Only working
            sessions can be bound.
          </Hint>
        </Field>

        {/* Verification modes */}
        <Field label="Verification modes">
          <div className="flex flex-wrap gap-2">
            <ModeToggle
              active={state.modes.includes("dm")}
              onClick={() => toggleMode("dm")}
              title="Direct message"
              subtitle="Proves number control"
            />
            <ModeToggle
              active={state.modes.includes("group")}
              onClick={() => toggleMode("group")}
              title="Group"
              subtitle="Proves group membership"
            />
          </div>
          {state.modes.length === 0 && (
            <p className="text-xs text-destructive">Enable at least one mode.</p>
          )}
          {state.modes.includes("group") && (
            <div className="mt-3 space-y-1.5">
              <Label htmlFor={`${idPrefix}-group`} className="text-sm">
                Pinned group
              </Label>
              {groupRows.length > 0 ? (
                <Select
                  value={state.groupJid}
                  onValueChange={(v) => set("groupJid", v)}
                >
                  <SelectTrigger id={`${idPrefix}-group`} className="w-full">
                    <SelectValue placeholder="Choose a group…" />
                  </SelectTrigger>
                  <SelectContent>
                    {groupRows.map((g) => (
                      <SelectItem key={g.groupJid} value={g.groupJid}>
                        {g.subject || g.groupJid}
                        {g.participants ? ` · ${g.participants} members` : ""}
                        {g.subject ? (
                          <span className="ml-1.5 font-mono text-xs text-muted-foreground">
                            {g.groupJid}
                          </span>
                        ) : null}
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
                  className="font-mono text-sm"
                />
              )}
              <Hint>
                {state.sessionId
                  ? groups.isLoading
                    ? "Loading the session's groups…"
                    : groupRows.length === 0
                      ? "No groups found on this session — paste the group JID directly."
                      : "The group members prove membership by messaging from inside it."
                  : "Pick a session first to list its groups."}
              </Hint>
            </div>
          )}
        </Field>

        <Separator />

        {/* Redirect URIs */}
        <Field label="Redirect URIs">
          <div className="space-y-2">
            {state.redirectUris.map((uri, i) => {
              const err = uri.trim() ? uriIssues.perUri[i] : null;
              const dup = uriIssues.duplicates.has(i);
              return (
                <div key={i} className="space-y-1">
                  <div className="flex items-center gap-2">
                    <Input
                      value={uri}
                      onChange={(e) => setUri(i, e.target.value)}
                      placeholder="https://app.example.com/callback"
                      className={cn(
                        "font-mono text-sm",
                        (err || dup) && "border-destructive",
                      )}
                      inputMode="url"
                      spellCheck={false}
                    />
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      onClick={() => removeUri(i)}
                      aria-label="Remove redirect URI"
                    >
                      <TrashIcon className="size-4" aria-hidden />
                    </Button>
                  </div>
                  {(err || dup) && (
                    <p className="text-xs text-destructive">
                      {err ?? "Duplicate URI."}
                    </p>
                  )}
                </div>
              );
            })}
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="mt-2 gap-1.5"
            onClick={addUri}
          >
            <PlusIcon className="size-4" aria-hidden />
            Add URI
          </Button>
          <Hint>
            Exact match, no wildcards. HTTPS required; <code>http://localhost</code>{" "}
            is allowed for development. Fragments (<code>#…</code>) are rejected.
          </Hint>
        </Field>

        <Separator />

        {/* Scopes */}
        <Field label="Scopes">
          <div className="space-y-2">
            {DASH_SCOPES.map((scope) => {
              const checked =
                scope.required || state.scopes.includes(scope.key);
              return (
                <label
                  key={scope.key}
                  className={cn(
                    "flex items-start gap-3 rounded-md border p-2.5 text-sm",
                    scope.required
                      ? "cursor-default bg-muted/40"
                      : "cursor-pointer hover:bg-muted/40",
                  )}
                >
                  <input
                    type="checkbox"
                    className="mt-0.5 size-4 accent-primary"
                    checked={checked}
                    disabled={scope.required}
                    onChange={() => toggleScope(scope.key)}
                  />
                  <span>
                    <span className="font-mono font-medium">{scope.label}</span>
                    {scope.required && (
                      <Badge variant="secondary" className="ml-2 text-[10px]">
                        required
                      </Badge>
                    )}
                    <span className="block text-xs text-muted-foreground">
                      {scope.description}
                    </span>
                  </span>
                </label>
              );
            })}
          </div>
        </Field>

        {/* Advanced */}
        <details className="rounded-lg border">
          <summary className="cursor-pointer select-none px-4 py-3 text-sm font-medium">
            Advanced
          </summary>
          <div className="space-y-4 border-t p-4">
            <Field label="Client type" htmlFor={`${idPrefix}-type`}>
              <Select
                value={state.clientType}
                onValueChange={(v) =>
                  set("clientType", v as OAuthClientType)
                }
              >
                <SelectTrigger id={`${idPrefix}-type`} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="confidential">
                    Confidential (server-side, has a secret)
                  </SelectItem>
                  <SelectItem value="public">
                    Public (SPA / mobile, PKCE only)
                  </SelectItem>
                </SelectContent>
              </Select>
              <Hint>
                {state.clientType === "public"
                  ? "No client secret is issued. The app authenticates with PKCE alone."
                  : "A client secret is shown once after creation. PKCE is still required."}
              </Hint>
            </Field>

            <TtlField
              label="Access / ID token lifetime"
              id={`${idPrefix}-ttl-token`}
              value={state.tokenTtlSeconds}
              min={TOKEN_TTL.min}
              max={TOKEN_TTL.max}
              onChange={(v) => set("tokenTtlSeconds", v)}
            />
            <TtlField
              label="Refresh token lifetime"
              id={`${idPrefix}-ttl-refresh`}
              value={state.refreshTtlSeconds}
              min={REFRESH_TTL.min}
              max={REFRESH_TTL.max}
              onChange={(v) => set("refreshTtlSeconds", v)}
            />
          </div>
        </details>
      </div>

      {/* Right: live consent preview */}
      {showPreview && (
        <div className="lg:sticky lg:top-4 lg:self-start">
          <ConsentPreview snapshot={previewSnapshot} />
        </div>
      )}
    </div>
  );
}

// --- Small building blocks --------------------------------------------------

function Field({
  label,
  htmlFor,
  optional,
  error,
  children,
}: {
  label: string;
  htmlFor?: string;
  optional?: boolean;
  error?: string | null;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-center gap-2">
        <Label htmlFor={htmlFor}>{label}</Label>
        {optional && (
          <span className="text-xs text-muted-foreground">optional</span>
        )}
      </div>
      {children}
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  );
}

function Hint({ children }: { children: React.ReactNode }) {
  return <p className="text-xs text-muted-foreground">{children}</p>;
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
        "flex flex-1 flex-col items-start gap-0.5 rounded-lg border p-3 text-left transition-colors",
        active
          ? "border-primary bg-primary/5"
          : "hover:border-muted-foreground/40",
      )}
    >
      <span className="flex w-full items-center justify-between">
        <span className="text-sm font-medium">{title}</span>
        {active && (
          <CheckCircle2Icon className="size-4 text-primary" aria-hidden />
        )}
      </span>
      <span className="text-xs text-muted-foreground">{subtitle}</span>
    </button>
  );
}

function TtlField({
  label,
  id,
  value,
  min,
  max,
  onChange,
}: {
  label: string;
  id: string;
  value: number;
  min: number;
  max: number;
  onChange: (v: number) => void;
}) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      <div className="flex items-center gap-2">
        <Input
          id={id}
          type="number"
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
      <Hint>
        Clamped to {humanizeSeconds(min)}–{humanizeSeconds(max)}.
      </Hint>
    </div>
  );
}
