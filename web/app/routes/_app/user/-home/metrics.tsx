// Home activity grid: one bordered surface split by hairlines into metric
// tiles (value + context line + sparkline), plus the Sessions tile, which
// reads live session statuses instead of the overview series.

import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { ChevronRightIcon, CircleCheckIcon, RefreshCwIcon } from "lucide-react";
import { Button } from "~/components/ui/button";
import { Skeleton } from "~/components/ui/skeleton";
import type { Overview } from "~/lib/api/hooks/overview";
import type { WASession } from "~/lib/api/types";
import { cn } from "~/lib/utils";
import { Sparkline, type SparkSeries } from "./sparkline";

const plain = new Intl.NumberFormat();
const compact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });
const pct = new Intl.NumberFormat(undefined, { style: "percent", maximumFractionDigits: 1 });

/** 1,284 below ten thousand; 12.9K above. */
export function formatCount(n: number): string {
  return n >= 10_000 ? compact.format(n) : plain.format(n);
}

/** The hairline grid every tile sits in (cells are divided by a 1px gap). */
export function MetricGrid({ busy, children }: { busy?: boolean; children: ReactNode }) {
  return (
    <div
      aria-busy={busy || undefined}
      className={cn(
        "grid gap-px overflow-hidden rounded-xl border bg-border shadow-[0_1px_2px_rgb(0_0_0/0.04)] sm:grid-cols-2 xl:grid-cols-3",
        "transition-opacity duration-300 ease-soft",
        busy && "opacity-60",
      )}
    >
      {children}
    </div>
  );
}

function Tile({
  label,
  to,
  value,
  detail,
  children,
  className,
}: {
  label: string;
  /** Makes the label a link to the related page. */
  to?: "/user/sessions" | "/user/webhooks";
  value: ReactNode;
  detail?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <section
      aria-label={label}
      className={cn("flex min-w-0 flex-col gap-3 bg-card p-5 sm:min-h-44", className)}
    >
      <div className="flex flex-col gap-1">
        <h3 className="text-sm text-muted-foreground">
          {to ? (
            <Link
              to={to}
              className="group/tile inline-flex items-center gap-0.5 rounded-sm transition-colors duration-150 ease-soft outline-none hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/30"
            >
              {label}
              <ChevronRightIcon
                aria-hidden
                className="size-3.5 transition-transform duration-150 ease-soft group-hover/tile:translate-x-0.5"
              />
            </Link>
          ) : (
            label
          )}
        </h3>
        <div className="text-2xl font-semibold tracking-tight">{value}</div>
        {detail ? <div className="text-xs text-muted-foreground">{detail}</div> : null}
      </div>
      {children ? <div className="mt-auto">{children}</div> : null}
    </section>
  );
}

function TileSkeleton() {
  return (
    <div className="flex min-h-40 flex-col gap-3 bg-card p-5 sm:min-h-44">
      <Skeleton className="h-4 w-28" />
      <Skeleton className="h-7 w-16" />
      <Skeleton className="mt-auto h-12 w-full" />
    </div>
  );
}

/** The five overview tiles, or skeletons while the first load runs. */
export function OverviewTiles({ data }: { data: Overview | undefined }) {
  if (!data) {
    return (
      <>
        {Array.from({ length: 5 }, (_, i) => (
          <TileSkeleton key={i} />
        ))}
      </>
    );
  }
  const { totals, series, buckets } = data;
  const hourly = data.range === "24h";
  const spark = (label: string, s: SparkSeries[]) => (
    <Sparkline label={label} buckets={buckets} series={s} hourly={hourly} />
  );

  const perChat =
    totals.activeChats > 0
      ? (totals.messagesReceived + totals.messagesSent) / totals.activeChats
      : 0;

  const attempts = totals.webhookDeliveries + totals.webhookFailures;
  const successRate = attempts > 0 ? totals.webhookDeliveries / attempts : null;

  return (
    <>
      <Tile
        label="Messages received"
        value={formatCount(totals.messagesReceived)}
        detail="Incoming messages on your numbers"
      >
        {spark("Messages received", [
          { label: "Received", values: series.messagesReceived, tone: "neutral" },
        ])}
      </Tile>

      <Tile
        label="Messages sent"
        value={formatCount(totals.messagesSent)}
        detail="From the API, the dashboard, and the phone"
      >
        {spark("Messages sent", [{ label: "Sent", values: series.messagesSent, tone: "neutral" }])}
      </Tile>

      <Tile
        label="Active chats"
        value={formatCount(totals.activeChats)}
        detail={
          totals.activeChats > 0
            ? `Chats with at least one message, about ${plain.format(Math.round(perChat))} messages each`
            : "Chats with at least one message"
        }
      />

      <Tile
        label="Send failures"
        value={formatCount(totals.sendsFailed)}
        detail={
          totals.sendsFailed === 0 ? (
            <span className="inline-flex items-center gap-1">
              <CircleCheckIcon aria-hidden className="size-3.5 text-emerald-600 dark:text-emerald-400" />
              Every send went through
            </span>
          ) : (
            "Sends that gave up after retrying"
          )
        }
      >
        {spark("Send failures", [{ label: "Failed", values: series.sendsFailed, tone: "danger" }])}
      </Tile>

      <Tile
        label="Webhook deliveries"
        to="/user/webhooks"
        value={formatCount(totals.webhookDeliveries)}
        detail={
          successRate === null ? (
            "Events posted to your webhook URLs"
          ) : (
            <span className="inline-flex flex-wrap items-center gap-x-2">
              <span className="text-foreground">{pct.format(successRate)} delivered</span>
              {totals.webhookFailures > 0 ? (
                <span className="inline-flex items-center gap-1.5">
                  <span aria-hidden className="h-0.5 w-2.5 rounded-full bg-destructive" />
                  {formatCount(totals.webhookFailures)} failed
                </span>
              ) : null}
            </span>
          )
        }
      >
        {spark("Webhook deliveries", [
          { label: "Delivered", values: series.webhookDeliveries, tone: "neutral" },
          ...(totals.webhookFailures > 0
            ? [{ label: "Failed", values: series.webhookFailures, tone: "danger" as const }]
            : []),
        ])}
      </Tile>
    </>
  );
}

/**
 * Stand-in for the five overview tiles when the activity endpoint can't be
 * read: spans their cells so the grid keeps its shape around the Sessions tile.
 */
export function OverviewUnavailable({
  missing,
  onRetry,
}: {
  /** True when the API predates GET /overview (404). */
  missing: boolean;
  onRetry: () => void;
}) {
  return (
    <div className="flex min-h-44 flex-col items-center justify-center gap-3 bg-card p-6 text-center sm:col-span-1 xl:col-span-2">
      <p className="max-w-sm text-sm text-pretty text-muted-foreground">
        {missing
          ? "Message activity needs a newer gateway API. Ask your gateway operator to update it; everything else on this page works."
          : "Couldn't load message activity."}
      </p>
      {missing ? null : (
        <Button variant="outline" size="sm" onClick={onRetry}>
          <RefreshCwIcon aria-hidden data-icon="inline-start" />
          Retry
        </Button>
      )}
    </div>
  );
}

// --- Sessions tile --------------------------------------------------------------

type Bucket = "connected" | "connecting" | "attention" | "stopped";

const BUCKET_OF: Record<WASession["status"], Bucket> = {
  working: "connected",
  starting: "connecting",
  scan_qr_code: "connecting",
  failed: "attention",
  logged_out: "attention",
  stopped: "stopped",
};

const BUCKET_META: Record<Bucket, { label: string; color: string }> = {
  connected: { label: "Connected", color: "bg-emerald-500" },
  connecting: { label: "Connecting", color: "bg-amber-500" },
  attention: { label: "Needs attention", color: "bg-destructive" },
  stopped: { label: "Stopped", color: "bg-muted-foreground/30" },
};

const BUCKET_ORDER: Bucket[] = ["connected", "connecting", "attention", "stopped"];

export function SessionsTile({
  sessions,
  more,
  loading,
}: {
  sessions: WASession[];
  /** More sessions exist beyond the loaded page. */
  more: boolean;
  loading: boolean;
}) {
  if (loading) return <TileSkeleton />;

  const counts: Record<Bucket, number> = { connected: 0, connecting: 0, attention: 0, stopped: 0 };
  for (const s of sessions) counts[BUCKET_OF[s.status]] += 1;
  const total = sessions.length;
  const totalLabel = `${plain.format(total)}${more ? "+" : ""}`;

  return (
    <Tile
      label="Sessions"
      to="/user/sessions"
      value={
        total === 0 ? (
          "0"
        ) : (
          <>
            {plain.format(counts.connected)}
            <span className="ml-1.5 text-sm font-normal tracking-normal text-muted-foreground">
              of {totalLabel} connected
            </span>
          </>
        )
      }
      detail={
        total === 0 ? (
          "Connect a WhatsApp number to start"
        ) : counts.attention > 0 ? (
          <Link
            to="/user/sessions"
            className="font-medium text-destructive underline-offset-4 hover:underline"
          >
            {counts.attention} {counts.attention === 1 ? "needs" : "need"} attention
          </Link>
        ) : (
          "Live status of your WhatsApp numbers"
        )
      }
    >
      {total > 0 ? (
        <div className="grid gap-2.5">
          <div
            role="img"
            aria-label={BUCKET_ORDER.filter((b) => counts[b] > 0)
              .map((b) => `${counts[b]} ${BUCKET_META[b].label.toLowerCase()}`)
              .join(", ")}
            className="flex h-2 gap-0.5 overflow-hidden rounded-full"
          >
            {BUCKET_ORDER.filter((b) => counts[b] > 0).map((b) => (
              <span
                key={b}
                className={cn("h-full first:rounded-l-full last:rounded-r-full", BUCKET_META[b].color)}
                style={{ flexGrow: counts[b] }}
              />
            ))}
          </div>
          <ul className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
            {BUCKET_ORDER.filter((b) => counts[b] > 0).map((b) => (
              <li key={b} className="inline-flex items-center gap-1.5">
                <span aria-hidden className={cn("size-1.5 rounded-full", BUCKET_META[b].color)} />
                {BUCKET_META[b].label}
                <span className="font-medium text-foreground tabular-nums">{counts[b]}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </Tile>
  );
}
