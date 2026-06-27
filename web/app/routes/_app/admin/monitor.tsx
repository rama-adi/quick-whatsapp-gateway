// Admin: global event monitor — live NDJSON firehose tail with type filtering.
// Reads the shared event bus (events=*, no session filter); it does NOT open its
// own connection — it opts into the single shared EventStreamProvider via
// useEventStreamSubscription() so the socket is live while this page is mounted.
// Ported from v1 admin/monitor.tsx; the route shell + ./-shared import path
// changed. Guard comes from the parent /admin route.

import { useCallback, useMemo, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { StreamIndicator, fmtTime } from "./-shared";
import { useEventBus } from "~/lib/events/eventBus";
import { useEventStreamSubscription } from "~/lib/events/useEventStream";
import type { EventEnvelope } from "~/lib/api/types";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
import { Input } from "~/components/ui/input";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
import { ScrollArea } from "~/components/ui/scroll-area";
import { cn } from "~/lib/utils";

export const Route = createFileRoute("/_app/admin/monitor")({
  component: AdminMonitor,
});

const MONITOR_CAPACITY = 500;

function AdminMonitor() {
  useEventStreamSubscription();
  const events = useEventBus(undefined, MONITOR_CAPACITY);
  const [typeFilter, setTypeFilter] = useState<string>("*");
  const [text, setText] = useState("");
  const [paused, setPaused] = useState(false);
  const [frozen, setFrozen] = useState<EventEnvelope[] | null>(null);
  const [selected, setSelected] = useState<EventEnvelope | null>(null);

  // When paused, render a frozen snapshot so the list stops moving.
  const source = paused ? (frozen ?? events) : events;

  // Distinct event types seen so far (for the filter dropdown).
  const eventTypes = useMemo(() => {
    const set = new Set<string>();
    for (const e of events) if (e.event) set.add(e.event);
    return Array.from(set).sort();
  }, [events]);

  const filtered = useMemo(() => {
    const needle = text.trim().toLowerCase();
    return source.filter((e) => {
      if (typeFilter !== "*" && e.event !== typeFilter) return false;
      if (!needle) return true;
      return (
        e.event?.toLowerCase().includes(needle) ||
        e.session?.toLowerCase().includes(needle) ||
        e.organization?.toLowerCase().includes(needle) ||
        e.id?.toLowerCase().includes(needle)
      );
    });
  }, [source, typeFilter, text]);

  const togglePause = useCallback(() => {
    setPaused((p) => {
      const next = !p;
      setFrozen(next ? events : null);
      return next;
    });
  }, [events]);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h1 className="text-xl font-semibold">Event Monitor</h1>
          <p className="text-sm text-muted-foreground">
            Live tail of the global event firehose (newest first, last{" "}
            {MONITOR_CAPACITY}).
          </p>
        </div>
        <StreamIndicator />
      </div>

      <Card>
        <CardHeader className="gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <Input
              type="search"
              placeholder="Filter by event, session, org, id…"
              value={text}
              onChange={(e) => setText(e.target.value)}
              className="w-full sm:w-72"
              aria-label="Filter events"
            />
            <select
              value={typeFilter}
              onChange={(e) => setTypeFilter(e.target.value)}
              aria-label="Filter by event type"
              className="h-9 rounded-md border border-input bg-transparent px-3 text-sm shadow-xs"
            >
              <option value="*">All event types</option>
              {eventTypes.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
            <Button variant={paused ? "default" : "outline"} size="sm" onClick={togglePause}>
              {paused ? "Resume" : "Pause"}
            </Button>
          </div>
          <CardDescription>
            {filtered.length} shown{paused ? " · paused" : ""}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {filtered.length === 0 ? (
            <p className="py-12 text-center text-sm text-muted-foreground">
              {events.length === 0
                ? "Waiting for events…"
                : "No events match the current filter."}
            </p>
          ) : (
            <ScrollArea className="h-[60vh] rounded-md border">
              <ul className="divide-y">
                {filtered.map((e) => (
                  <li key={e.id}>
                    <button
                      type="button"
                      onClick={() => setSelected(e)}
                      className={cn(
                        "flex w-full items-center gap-3 px-3 py-2 text-left text-sm hover:bg-accent",
                        selected?.id === e.id && "bg-accent",
                      )}
                    >
                      <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
                        {fmtTime(e.timestamp)}
                      </span>
                      <Badge variant="outline" className="shrink-0 font-mono">
                        {e.event}
                      </Badge>
                      <span className="truncate font-mono text-xs text-muted-foreground">
                        {e.session || e.organization || e.id}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </ScrollArea>
          )}
        </CardContent>
      </Card>

      {selected && <EventDetail event={selected} onClose={() => setSelected(null)} />}
    </div>
  );
}

function EventDetail({ event, onClose }: { event: EventEnvelope; onClose: () => void }) {
  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between gap-2 space-y-0">
        <div>
          <CardTitle className="text-base">{event.event}</CardTitle>
          <CardDescription className="font-mono text-xs">{event.id}</CardDescription>
        </div>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close detail">
          Close
        </Button>
      </CardHeader>
      <CardContent className="space-y-3">
        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm sm:grid-cols-4">
          <Meta label="schema" value={event.schema} />
          <Meta label="session" value={event.session} mono />
          <Meta label="org" value={event.organization} mono />
          <Meta label="time" value={fmtTime(event.timestamp)} />
        </dl>
        <div>
          <div className="mb-1 text-xs font-medium text-muted-foreground">payload</div>
          <pre className="max-h-72 overflow-auto rounded-md border bg-muted/40 p-3 text-xs">
            {JSON.stringify(event.payload, null, 2)}
          </pre>
        </div>
      </CardContent>
    </Card>
  );
}

function Meta({ label, value, mono }: { label: string; value?: string; mono?: boolean }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={cn("truncate", mono && "font-mono text-xs")}>{value || "—"}</dd>
    </div>
  );
}
