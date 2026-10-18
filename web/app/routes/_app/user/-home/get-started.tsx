// The "Get started" checklist: four setup steps derived from live data
// (sessions, API keys, sent messages, webhooks). Each step expands to a short
// explanation and the button that does it; the first unfinished step starts
// open. The card can be collapsed or dismissed (remembered per organization in
// localStorage) and collapses on its own once every step is done.

import { useState, useSyncExternalStore, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { ArrowUpRightIcon, CheckIcon, ChevronDownIcon, XIcon } from "lucide-react";
import { CodeSnippet } from "~/components/code-snippet";
import { Button } from "~/components/ui/button";
import { Skeleton } from "~/components/ui/skeleton";
import { usePublicApiUrl } from "~/lib/api/public-url";
import type { WASession } from "~/lib/api/types";
import { DOCS } from "~/lib/docs-links";
import { apiRequestSnippets } from "~/lib/snippets";
import { cn } from "~/lib/utils";
import { ArtPanel, QuickLink } from "./art-panel";
import { sessionTitle } from "~/lib/format";

// --- Per-org visibility, persisted in localStorage ------------------------------

export type ChecklistPref = "collapsed" | "dismissed";

const PREF_EVENT = "qwg:get-started-pref";
const prefKey = (orgId: string) => `qwg.home.getStarted.${orgId}`;

function readPref(orgId: string): ChecklistPref | null {
  try {
    const v = window.localStorage.getItem(prefKey(orgId));
    return v === "collapsed" || v === "dismissed" ? v : null;
  } catch {
    return null;
  }
}

function subscribePref(cb: () => void): () => void {
  window.addEventListener("storage", cb);
  window.addEventListener(PREF_EVENT, cb);
  return () => {
    window.removeEventListener("storage", cb);
    window.removeEventListener(PREF_EVENT, cb);
  };
}

/** The stored checklist preference for an org, and a setter (null clears it). */
export function useChecklistPref(
  orgId: string,
): [ChecklistPref | null, (next: ChecklistPref | null) => void] {
  const pref = useSyncExternalStore(
    subscribePref,
    () => readPref(orgId),
    () => null,
  );
  const set = (next: ChecklistPref | null) => {
    try {
      if (next) window.localStorage.setItem(prefKey(orgId), next);
      else window.localStorage.removeItem(prefKey(orgId));
    } catch {
      // Storage unavailable (private mode): the choice lasts until reload.
    }
    window.dispatchEvent(new Event(PREF_EVENT));
  };
  return [pref, set];
}

// --- Checklist -------------------------------------------------------------------

export interface ChecklistState {
  /** Undefined while that source is still loading. */
  connected: boolean | undefined;
  hasKey: boolean | undefined;
  hasSent: boolean | undefined;
  hasWebhook: boolean | undefined;
}

type StepId = "connect" | "key" | "send" | "webhook";

interface Step {
  id: StepId;
  title: string;
  done: boolean | undefined;
  body: ReactNode;
  action: ReactNode;
}

const linkClass =
  "font-medium text-foreground/80 underline-offset-4 hover:text-foreground hover:underline";

export function GetStarted({
  state,
  firstWorking,
  pref,
  onPref,
}: {
  state: ChecklistState;
  /** The first connected session, prefilled into the send snippet. */
  firstWorking: WASession | undefined;
  pref: ChecklistPref | null;
  onPref: (next: ChecklistPref | null) => void;
}) {
  const apiBase = usePublicApiUrl();
  const sessionId = firstWorking?.id ?? "$SESSION_ID";
  const to = firstWorking?.phoneNumber
    ? `${firstWorking.phoneNumber}@s.whatsapp.net`
    : "628123456789@s.whatsapp.net";

  const steps: Step[] = [
    {
      id: "connect",
      title: "Connect a WhatsApp number",
      done: state.connected,
      body: "Link a phone by scanning a QR code, the same way you'd open WhatsApp Web. The gateway keeps the connection running, so your phone doesn't need to stay online.",
      action: (
        <Button asChild size="sm">
          <Link to="/user/sessions">Connect a number</Link>
        </Button>
      ),
    },
    {
      id: "key",
      title: "Create an API key",
      done: state.hasKey,
      body: "Your code sends the key with every request so the gateway knows which organization it's acting for. Turn on the send permission if the key will send messages.",
      action: (
        <Button asChild size="sm">
          <Link to="/user/keys">Create an API key</Link>
        </Button>
      ),
    },
    {
      id: "send",
      title: "Send your first message",
      done: state.hasSent,
      body: (
        <div className="grid gap-3">
          <p>
            Run this from a terminal or your app.{" "}
            {firstWorking ? (
              <>
                It sends a test message to your own number from{" "}
                <span className="font-medium text-foreground">
                  {sessionTitle(firstWorking)}
                </span>
                .
              </>
            ) : (
              <>
                Replace <code className="font-mono text-xs">$SESSION_ID</code> with a
                connected session.
              </>
            )}{" "}
            Replace <code className="font-mono text-xs">$WA_API_KEY</code> with a key
            from{" "}
            <Link to="/user/keys" className={linkClass}>
              API keys
            </Link>
            .
          </p>
          <CodeSnippet
            tabs={apiRequestSnippets({
              method: "POST",
              url: `${apiBase}/sessions/${sessionId}/messages`,
              token: "$WA_API_KEY",
              body: { type: "text", to, text: "Hello from WA Gateway" },
            })}
          />
        </div>
      ),
      action: (
        <Button asChild size="sm" variant="outline">
          <a href={DOCS.sending} target="_blank" rel="noreferrer">
            Sending guide
            <ArrowUpRightIcon aria-hidden data-icon="inline-end" />
          </a>
        </Button>
      ),
    },
    {
      id: "webhook",
      title: "Receive messages with a webhook",
      done: state.hasWebhook,
      body: "Give the gateway a URL on your server. It posts every new message and delivery update there as JSON, signed so you can check it came from the gateway.",
      action: (
        <Button asChild size="sm">
          <Link to="/user/webhooks">Add a webhook</Link>
        </Button>
      ),
    },
  ];

  const loading = steps.some((s) => s.done === undefined);
  const doneCount = steps.filter((s) => s.done).length;
  const allDone = !loading && doneCount === steps.length;
  const firstOpen = steps.find((s) => s.done === false)?.id ?? null;

  // Explicit opens this visit win over the auto-collapse of a finished list.
  const [forcedOpen, setForcedOpen] = useState(false);
  const [openStep, setOpenStep] = useState<StepId | null | undefined>(undefined);
  const expandedStep = openStep === undefined ? firstOpen : openStep;

  if (pref === "dismissed") return null;
  const collapsed = pref === "collapsed" || (allDone && !forcedOpen);

  const toggleCollapsed = () => {
    if (collapsed) {
      setForcedOpen(true);
      onPref(null);
    } else {
      setForcedOpen(false);
      onPref("collapsed");
    }
  };

  return (
    <section aria-labelledby="get-started-title" className="grid gap-3">
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <h2 id="get-started-title" className="text-base font-semibold tracking-tight whitespace-nowrap">
            Get started
          </h2>
          {loading ? (
            <Skeleton className="h-4 w-20" />
          ) : (
            <Progress done={doneCount} total={steps.length} />
          )}
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button
            variant="ghost"
            size="sm"
            aria-expanded={!collapsed}
            aria-controls="get-started-body"
            onClick={toggleCollapsed}
            className="text-muted-foreground"
          >
            <ChevronDownIcon
              aria-hidden
              data-icon="inline-start"
              className={cn("transition-transform duration-200 ease-soft", !collapsed && "rotate-180")}
            />
            {collapsed ? "Show" : "Hide"}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => onPref("dismissed")}
            className="text-muted-foreground"
          >
            <XIcon aria-hidden data-icon="inline-start" />
            <span className="max-sm:sr-only">Dismiss</span>
          </Button>
        </div>
      </div>

      {collapsed ? null : (
        <div
          id="get-started-body"
          className="grid animate-in overflow-hidden rounded-xl border bg-card shadow-[0_1px_2px_rgb(0_0_0/0.04)] duration-300 ease-soft fade-in-0 lg:grid-cols-[minmax(0,1fr)_minmax(0,0.85fr)]"
        >
          <div className="flex flex-col p-2">
            <ol className="grid gap-0.5">
              {steps.map((step, i) => (
                <StepRow
                  key={step.id}
                  index={i + 1}
                  step={step}
                  open={expandedStep === step.id}
                  onToggle={() => setOpenStep(expandedStep === step.id ? null : step.id)}
                />
              ))}
            </ol>
            <div className="mt-2 grid gap-2 border-t px-1 pt-3 pb-1 sm:grid-cols-2 lg:hidden">
              <QuickLink href={DOCS.sending} title="API quickstart" description="Send your first message from code" />
              <QuickLink href={DOCS.apiReference} title="API reference" description="Every endpoint, field, and event" />
            </div>
          </div>
          <ArtPanel />
        </div>
      )}
    </section>
  );
}

function Progress({ done, total }: { done: number; total: number }) {
  return (
    <span className="flex items-center gap-2 text-xs whitespace-nowrap text-muted-foreground">
      <span
        role="progressbar"
        aria-label="Setup progress"
        aria-valuemin={0}
        aria-valuemax={total}
        aria-valuenow={done}
        className="hidden h-1.5 w-16 overflow-hidden rounded-full bg-muted sm:flex"
      >
        <span
          className="h-full rounded-full bg-emerald-500 transition-[width] duration-300 ease-soft"
          style={{ width: `${(done / total) * 100}%` }}
        />
      </span>
      <span className="tabular-nums">
        {done} of {total} done
      </span>
    </span>
  );
}

function StepRow({
  index,
  step,
  open,
  onToggle,
}: {
  index: number;
  step: Step;
  open: boolean;
  onToggle: () => void;
}) {
  const panelId = `step-${step.id}`;
  return (
    <li className={cn("rounded-lg transition-colors duration-150 ease-soft", open && "bg-muted/50")}>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={panelId}
        onClick={onToggle}
        className="flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left outline-none hover:bg-muted/60 focus-visible:ring-3 focus-visible:ring-ring/30"
      >
        <StepMarker index={index} done={step.done} />
        <span
          className={cn(
            "min-w-0 flex-1 text-sm font-medium",
            step.done && "text-muted-foreground",
          )}
        >
          {step.title}
        </span>
        {step.done ? (
          <span className="text-xs font-medium text-emerald-700 dark:text-emerald-400">Done</span>
        ) : step.done === false ? (
          <span className="sr-only">Not done yet</span>
        ) : null}
        <ChevronDownIcon
          aria-hidden
          className={cn(
            "size-4 shrink-0 text-muted-foreground transition-transform duration-200 ease-soft",
            open && "rotate-180",
          )}
        />
      </button>
      {open ? (
        <div
          id={panelId}
          className="grid animate-in gap-3 pr-3 pb-3 pl-12 text-sm text-pretty text-muted-foreground duration-200 ease-soft fade-in-0 slide-in-from-top-1"
        >
          {typeof step.body === "string" ? <p>{step.body}</p> : step.body}
          <div className="flex flex-wrap gap-2">{step.action}</div>
        </div>
      ) : null}
    </li>
  );
}

function StepMarker({ index, done }: { index: number; done: boolean | undefined }) {
  if (done === undefined) {
    return <Skeleton className="size-6 shrink-0 rounded-full" />;
  }
  return (
    <span
      aria-hidden
      className={cn(
        "flex size-6 shrink-0 items-center justify-center rounded-full text-xs font-semibold tabular-nums transition-colors duration-200 ease-soft",
        done
          ? "bg-emerald-500/12 text-emerald-700 dark:text-emerald-400"
          : "bg-background text-muted-foreground ring-1 ring-border",
      )}
    >
      {done ? <CheckIcon className="size-3.5" strokeWidth={2.5} /> : index}
    </span>
  );
}
