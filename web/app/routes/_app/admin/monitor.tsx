// Admin: global event monitor — a live tail of the platform-wide event stream.
// It does NOT open its own connection: it opts into the single shared
// EventStreamProvider via useEventStreamSubscription() and keeps its own
// bounded ring buffer off the raw event bus (so it can be cleared, and so it
// can count what arrived while paused). Guard comes from the parent /admin route.

import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { createFileRoute } from "@tanstack/react-router";
import {
  ActivityIcon,
  EraserIcon,
  MousePointerClickIcon,
  PauseIcon,
  PlayIcon,
  SearchIcon,
  XIcon,
} from "lucide-react";
import { StreamIndicator, TONE_CLASS, useOrgNames } from "./-shared";
import type { Tone } from "./-gateway-reconciliation";
import { subscribeEvents } from "~/lib/events/eventBus";
import { useEventStreamSubscription } from "~/lib/events/useEventStream";
import { useAdminSessions } from "~/lib/api/hooks/admin";
import type { EventEnvelope } from "~/lib/api/types";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "~/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "~/components/ui/sheet";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "~/components/ui/empty";
import { TableCard } from "~/components/ui/table";
import { Page, PageTitle } from "~/components/shell/page-title";
import { CodeSnippet } from "~/components/code-snippet";
import { CopyButton, CopyableId } from "~/components/copyable";
import { sessionStatusLabel } from "~/components/status-badge";
import { formatDateTime, middleTruncate } from "~/lib/format";
import { titleHead } from "~/lib/head";
import { DOCS } from "~/lib/docs-links";
import type { SessionStatus } from "~/lib/api/types";
import { cn } from "~/lib/utils";

export const Route = createFileRoute("/_app/admin/monitor")({
  head: () => titleHead("Event monitor"),
  component: AdminMonitor,
});

const MONITOR_CAPACITY = 500;

const CLOCK = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });

function clock(ts: number | undefined): string {
  return ts ? CLOCK.format(new Date(ts)) : "—";
}

/** Color family per event namespace, so the feed is scannable at a glance. */
function eventTone(type: string | undefined): Tone {
  const ns = type?.split(".")[0];
  switch (ns) {
    case "message":
    case "media":
      return "info";
    case "session":
      return "ok";
    case "auth":
      return "warn";
    case "call":
      return "bad";
    default:
      return "muted";
  }
}

/** A short human hint from the payload (status, text, …) for the feed row. */
function summarize(e: EventEnvelope): string {
  const p = e.payload as Record<string, unknown> | undefined;
  if (!p || typeof p !== "object") return "";
  if (e.event === "session.status" && typeof p.status === "string") {
    return `→ ${sessionStatusLabel(p.status as SessionStatus)}`;
  }
  for (const key of ["text", "body", "caption", "status", "type", "action", "name"]) {
    const v = p[key];
    if (typeof v === "string" && v.trim()) return v.length > 80 ? `${v.slice(0, 80)}…` : v;
  }
  return "";
}

/** Live ring buffer over the raw bus; keeps counting while paused. */
function useMonitorBuffer() {
  const [events, setEvents] = useState<EventEnvelope[]>([]);
  const [received, setReceived] = useState(0);
  useEffect(
    () =>
      subscribeEvents((e) => {
        setEvents((prev) => {
          const next = [e, ...prev];
          return next.length > MONITOR_CAPACITY ? next.slice(0, MONITOR_CAPACITY) : next;
        });
        setReceived((n) => n + 1);
      }),
    [],
  );
  return { events, received, clear: () => setEvents([]) };
}

const LG_QUERY = "(min-width: 1024px)";
function useIsLarge(): boolean {
  return useSyncExternalStore(
    (cb) => {
      const mq = window.matchMedia(LG_QUERY);
      mq.addEventListener("change", cb);
      return () => mq.removeEventListener("change", cb);
    },
    () => window.matchMedia(LG_QUERY).matches,
    () => true,
  );
}

function AdminMonitor() {
  useEventStreamSubscription();
  const { events, received, clear } = useMonitorBuffer();
  const sessions = useAdminSessions();
  const orgNames = useOrgNames();
  const isLarge = useIsLarge();
  const [typeFilter, setTypeFilter] = useState<string>("*");
  const [text, setText] = useState("");
  const [paused, setPaused] = useState<{ events: EventEnvelope[]; at: number } | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const listRef = useRef<HTMLDivElement>(null);

  const sessionLabels = useMemo(() => {
    const map = new Map<string, string>();
    for (const page of sessions.data?.pages ?? []) {
      for (const s of page.data ?? []) if (s.label) map.set(s.id, s.label);
    }
    return map;
  }, [sessions.data]);

  // While paused, render the snapshot taken at pause time so the list stops moving.
  const source = paused ? paused.events : events;
  const missed = paused ? received - paused.at : 0;

  const eventTypes = useMemo(() => {
    const set = new Set<string>();
    for (const e of events) if (e.event) set.add(e.event);
    if (typeFilter !== "*") set.add(typeFilter);
    return Array.from(set).sort();
  }, [events, typeFilter]);

  const filtered = useMemo(() => {
    const needle = text.trim().toLowerCase();
    return source.filter((e) => {
      if (typeFilter !== "*" && e.event !== typeFilter) return false;
      if (!needle) return true;
      return [
        e.event,
        e.session,
        e.organization,
        e.id,
        e.session ? sessionLabels.get(e.session) : undefined,
        e.organization ? orgNames.get(e.organization) : undefined,
      ].some((v) => v?.toLowerCase().includes(needle));
    });
  }, [source, typeFilter, text, sessionLabels, orgNames]);

  const selected = useMemo(
    () => (selectedId ? (source.find((e) => e.id === selectedId) ?? events.find((e) => e.id === selectedId) ?? null) : null),
    [selectedId, source, events],
  );

  const pause = () => setPaused({ events, at: received });
  const resume = () => {
    setPaused(null);
    listRef.current?.scrollTo({ top: 0 });
  };
  const filtering = typeFilter !== "*" || Boolean(text.trim());

  return (
    <Page>
      <PageTitle
        title="Event monitor"
        description="A live feed of every event on the platform, newest first."
        docsHref={DOCS.eventShape}
        actions={<StreamIndicator />}
      />

      <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
        <div className="relative w-full sm:w-72">
          <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
          <Input
            type="search"
            placeholder="Filter by event, session, org, ID…"
            value={text}
            onChange={(e) => setText(e.target.value)}
            className="pl-8"
            aria-label="Filter events"
          />
        </div>
        <Select value={typeFilter} onValueChange={setTypeFilter}>
          <SelectTrigger className="w-full sm:w-56" aria-label="Filter by event type">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="*">All event types</SelectItem>
            {eventTypes.map((t) => (
              <SelectItem key={t} value={t}>
                <span className="font-mono text-xs">{t}</span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {filtering ? (
          <Button
            variant="ghost"
            onClick={() => {
              setText("");
              setTypeFilter("*");
            }}
          >
            <XIcon aria-hidden />
            Clear filters
          </Button>
        ) : null}
        <div className="flex items-center gap-2 sm:ml-auto">
          <span className="text-sm text-muted-foreground tabular-nums">
            {filtering ? `${filtered.length} of ${source.length}` : source.length} · last {MONITOR_CAPACITY} kept
          </span>
          {paused ? (
            <Button onClick={resume}>
              <PlayIcon aria-hidden />
              Resume
            </Button>
          ) : (
            <Button variant="outline" onClick={pause}>
              <PauseIcon aria-hidden />
              Pause
            </Button>
          )}
          <Button
            variant="outline"
            disabled={events.length === 0}
            onClick={() => {
              clear();
              setPaused((p) => (p ? { events: [], at: received } : p));
              setSelectedId(null);
            }}
          >
            <EraserIcon aria-hidden />
            Clear
          </Button>
        </div>
      </div>

      <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_26rem]">
        <TableCard className="flex flex-col">
          {paused ? (
            <div className="flex items-center justify-between gap-3 border-b bg-amber-500/10 px-4 py-2 text-sm text-amber-800 dark:text-amber-300">
              <span>
                Paused.{" "}
                {missed > 0
                  ? `${missed} new event${missed === 1 ? "" : "s"} since you paused.`
                  : "No new events yet."}
              </span>
              <Button size="sm" variant="ghost" className="text-inherit hover:bg-amber-500/15" onClick={resume}>
                Jump to live
              </Button>
            </div>
          ) : null}
          {filtered.length === 0 ? (
            <Empty className="h-[60svh] border-0">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <ActivityIcon aria-hidden />
                </EmptyMedia>
                <EmptyTitle>{source.length === 0 ? "Waiting for events" : "No events match"}</EmptyTitle>
                <EmptyDescription>
                  {source.length === 0
                    ? "Events show up here as they happen: messages, session status changes, QR codes and more."
                    : "Change the filters to see more of the feed."}
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <div ref={listRef} className="h-[60svh] overflow-y-auto lg:h-[calc(100svh-17rem)] lg:min-h-96">
              <ul className="divide-y">
                {filtered.map((e) => {
                  const tone = TONE_CLASS[eventTone(e.event)];
                  const summary = summarize(e);
                  const who = e.session
                    ? (sessionLabels.get(e.session) ?? middleTruncate(e.session))
                    : e.organization
                      ? (orgNames.get(e.organization) ?? middleTruncate(e.organization))
                      : "";
                  return (
                    <li key={e.id}>
                      <button
                        type="button"
                        onClick={() => setSelectedId(e.id)}
                        aria-pressed={selected?.id === e.id}
                        className={cn(
                          "grid w-full grid-cols-[5.75rem_minmax(0,1fr)] items-center gap-x-3 px-4 py-2 text-left text-sm transition-colors duration-150 ease-soft hover:bg-muted/60 sm:grid-cols-[5.75rem_13rem_minmax(0,1fr)]",
                          selected?.id === e.id && "bg-muted",
                        )}
                      >
                        <span className="text-xs whitespace-nowrap text-muted-foreground tabular-nums" title={formatDateTime(e.timestamp, { seconds: true })}>
                          {clock(e.timestamp)}
                        </span>
                        <span className="flex min-w-0 items-center gap-2">
                          <span aria-hidden className={cn("size-1.5 shrink-0 rounded-full", tone.dot)} />
                          <span className="truncate font-mono text-xs font-medium">{e.event}</span>
                        </span>
                        <span className="col-start-2 truncate text-xs text-muted-foreground sm:col-start-auto">
                          {who}
                          {who && summary ? " · " : ""}
                          {summary}
                        </span>
                      </button>
                    </li>
                  );
                })}
              </ul>
            </div>
          )}
        </TableCard>

        {isLarge ? (
          <aside className="sticky top-0 min-w-0 rounded-xl border bg-card shadow-[0_1px_2px_rgb(0_0_0/0.04)] dark:shadow-none">
            {selected ? (
              <EventDetail
                event={selected}
                sessionLabel={selected.session ? sessionLabels.get(selected.session) : undefined}
                orgName={selected.organization ? orgNames.get(selected.organization) : undefined}
                onClose={() => setSelectedId(null)}
              />
            ) : (
              <Empty className="border-0">
                <EmptyHeader>
                  <EmptyMedia variant="icon">
                    <MousePointerClickIcon aria-hidden />
                  </EmptyMedia>
                  <EmptyTitle>No event selected</EmptyTitle>
                  <EmptyDescription>Pick an event in the feed to see its full payload.</EmptyDescription>
                </EmptyHeader>
              </Empty>
            )}
          </aside>
        ) : (
          <Sheet open={Boolean(selected)} onOpenChange={(open) => !open && setSelectedId(null)}>
            <SheetContent side="bottom" className="max-h-[85svh] overflow-y-auto rounded-t-2xl">
              {selected ? (
                <EventDetail
                  event={selected}
                  sessionLabel={selected.session ? sessionLabels.get(selected.session) : undefined}
                  orgName={selected.organization ? orgNames.get(selected.organization) : undefined}
                  inSheet
                />
              ) : null}
            </SheetContent>
          </Sheet>
        )}
      </div>
    </Page>
  );
}

function EventDetail({
  event,
  sessionLabel,
  orgName,
  onClose,
  inSheet = false,
}: {
  event: EventEnvelope;
  sessionLabel?: string;
  orgName?: string;
  onClose?: () => void;
  inSheet?: boolean;
}) {
  const tone = TONE_CLASS[eventTone(event.event)];
  const heading = (
    <span className="flex min-w-0 items-center gap-2">
      <span aria-hidden className={cn("size-2 shrink-0 rounded-full", tone.dot)} />
      <span className="truncate font-mono text-sm">{event.event}</span>
    </span>
  );
  return (
    <div className="grid gap-4 p-4">
      {inSheet ? (
        <SheetHeader className="p-0 pr-8">
          <SheetTitle>{heading}</SheetTitle>
          <SheetDescription>{formatDateTime(event.timestamp, { seconds: true })}</SheetDescription>
        </SheetHeader>
      ) : (
        <div className="flex items-start justify-between gap-2">
          <div className="grid min-w-0 gap-0.5">
            <h2 className="font-semibold">{heading}</h2>
            <p className="text-xs text-muted-foreground">{formatDateTime(event.timestamp, { seconds: true })}</p>
          </div>
          {onClose ? (
            <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label="Close event detail">
              <XIcon aria-hidden />
            </Button>
          ) : null}
        </div>
      )}

      <dl className="grid gap-2 text-sm">
        <Meta label="Event ID">
          <CopyableId value={event.id} truncate label="Copy event ID" />
        </Meta>
        <Meta label="Session">
          {event.session ? (
            <span className="flex min-w-0 flex-wrap items-center gap-x-2">
              {sessionLabel ? <span className="truncate">{sessionLabel}</span> : null}
              <span className="text-muted-foreground">
                <CopyableId value={event.session} truncate label="Copy session ID" />
              </span>
            </span>
          ) : (
            "—"
          )}
        </Meta>
        <Meta label="Organization">
          {event.organization ? (
            <span className="flex min-w-0 flex-wrap items-center gap-x-2">
              {orgName ? <span className="truncate">{orgName}</span> : null}
              <span className="text-muted-foreground">
                <CopyableId value={event.organization} truncate label="Copy organization ID" />
              </span>
            </span>
          ) : (
            "—"
          )}
        </Meta>
        <Meta label="Schema">
          <span className="font-mono text-xs">{event.schema}</span>
        </Meta>
      </dl>

      <CodeSnippet title="Payload" code={JSON.stringify(event.payload, null, 2)} copyLabel="Copy payload" />
      <CopyButton value={JSON.stringify(event, null, 2)} label="Copy full event JSON" className="justify-self-start" />
    </div>
  );
}

function Meta({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[6.5rem_minmax(0,1fr)] items-center gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}
