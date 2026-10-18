// Inline SVG sparkline for the Home metric tiles. No chart library: one or two
// thin lines over a zero baseline, a soft area under the first series, a dot on
// the latest bucket, and a crosshair + tooltip on hover or keyboard focus
// (arrow keys step through buckets). Values and labels render as text, never
// in the series color.

import { useId, useMemo, useState, type KeyboardEvent, type PointerEvent } from "react";
import { cn } from "~/lib/utils";

export type SparkTone = "neutral" | "danger";

export interface SparkSeries {
  label: string;
  values: readonly number[];
  tone: SparkTone;
}

const TONE_TEXT: Record<SparkTone, string> = {
  neutral: "text-foreground/70",
  danger: "text-destructive",
};

/** viewBox height; x runs 0..100 and the SVG stretches to the tile width. */
const H = 40;
const PAD_TOP = 4;
const PAD_BOTTOM = 2;

const numberFmt = new Intl.NumberFormat();

/** "Sat, Oct 18" for daily buckets, "Oct 18, 14:00" for hourly ones (local time). */
export function formatBucket(ms: number, hourly: boolean): string {
  const d = new Date(ms);
  return hourly
    ? d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })
    : d.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" });
}

export function Sparkline({
  label,
  buckets,
  series,
  hourly,
  className,
}: {
  /** What the chart shows, e.g. "Messages received"; leads the accessible summary. */
  label: string;
  /** Start of each bucket (epoch ms); aligned with every series' values. */
  buckets: readonly number[];
  series: readonly SparkSeries[];
  hourly: boolean;
  className?: string;
}) {
  const gradientId = useId();
  const [active, setActive] = useState<number | null>(null);
  const n = buckets.length;

  const geo = useMemo(() => {
    const max = Math.max(1, ...series.flatMap((s) => s.values));
    const x = (i: number) => (n <= 1 ? 50 : (i / (n - 1)) * 100);
    const y = (v: number) => H - PAD_BOTTOM - (v / max) * (H - PAD_TOP - PAD_BOTTOM);
    const paths = series.map((s) =>
      s.values.map((v, i) => `${i === 0 ? "M" : "L"}${x(i).toFixed(2)},${y(v).toFixed(2)}`).join(""),
    );
    return { x, y, paths };
  }, [series, n]);

  const empty = series.every((s) => s.values.every((v) => v === 0));
  const primary = series[0];

  const summary = useMemo(() => {
    if (!primary || n === 0) return `${label}: no data`;
    const total = primary.values.reduce((a, b) => a + b, 0);
    let peak = 0;
    primary.values.forEach((v, i) => {
      if (v > (primary.values[peak] ?? 0)) peak = i;
    });
    const span = hourly ? `last ${n} hours` : `last ${n} days`;
    if (total === 0) return `${label}, ${span}: none`;
    return `${label}, ${span}: ${numberFmt.format(total)} in total, peak ${numberFmt.format(
      primary.values[peak] ?? 0,
    )} on ${formatBucket(buckets[peak] ?? 0, hourly)}`;
  }, [primary, n, label, hourly, buckets]);

  const pick = (e: PointerEvent<HTMLDivElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    if (r.width === 0 || n === 0) return;
    const ratio = Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
    setActive(Math.round(ratio * (n - 1)));
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (n === 0) return;
    const cur = active ?? n - 1;
    const next =
      e.key === "ArrowLeft" ? Math.max(0, cur - 1)
      : e.key === "ArrowRight" ? Math.min(n - 1, cur + 1)
      : e.key === "Home" ? 0
      : e.key === "End" ? n - 1
      : null;
    if (next === null) return;
    e.preventDefault();
    setActive(next);
  };

  const ax = active !== null ? geo.x(active) : 0;
  // Keep the tooltip inside the tile near the edges.
  const align = ax < 20 ? "left" : ax > 80 ? "right" : "center";
  const isLast = active === n - 1;

  return (
    <div
      role="group"
      aria-roledescription="sparkline"
      aria-label={summary}
      tabIndex={0}
      onPointerMove={pick}
      onPointerDown={pick}
      onPointerLeave={() => setActive(null)}
      onFocus={() => setActive((a) => a ?? n - 1)}
      onBlur={() => setActive(null)}
      onKeyDown={onKeyDown}
      className={cn(
        "relative h-12 w-full touch-pan-y rounded-md outline-none focus-visible:ring-3 focus-visible:ring-ring/30",
        className,
      )}
    >
      <svg
        viewBox={`0 0 100 ${H}`}
        preserveAspectRatio="none"
        aria-hidden
        className="absolute inset-0 size-full overflow-visible"
      >
        <defs>
          <linearGradient id={gradientId} x1="0" x2="0" y1="0" y2="1">
            <stop offset="0%" stopColor="currentColor" stopOpacity="0.12" />
            <stop offset="100%" stopColor="currentColor" stopOpacity="0" />
          </linearGradient>
        </defs>
        {empty ? (
          <line
            x1="0"
            x2="100"
            y1={H - PAD_BOTTOM}
            y2={H - PAD_BOTTOM}
            className="stroke-border"
            strokeWidth={2}
            strokeDasharray="6 5"
            vectorEffect="non-scaling-stroke"
          />
        ) : (
          <>
            {primary && n > 1 ? (
              <path
                d={`${geo.paths[0]}L100,${H}L0,${H}Z`}
                fill={`url(#${gradientId})`}
                className={TONE_TEXT[primary.tone]}
              />
            ) : null}
            {/* Later series draw first so the primary line sits on top. */}
            {series
              .map((s, i) => ({ s, d: geo.paths[i] }))
              .reverse()
              .map(({ s, d }) => (
                <path
                  key={s.label}
                  d={d}
                  fill="none"
                  stroke="currentColor"
                  strokeWidth={2}
                  strokeLinejoin="round"
                  strokeLinecap="round"
                  vectorEffect="non-scaling-stroke"
                  className={TONE_TEXT[s.tone]}
                />
              ))}
          </>
        )}
      </svg>

      {/* Latest bucket marker (HTML so it stays round on a stretched SVG). */}
      {!empty && primary && n > 0 && active === null ? (
        <Dot x={geo.x(n - 1)} y={geo.y(primary.values[n - 1] ?? 0)} tone={primary.tone} />
      ) : null}

      {active !== null ? (
        <>
          <div
            aria-hidden
            className="pointer-events-none absolute inset-y-0 w-px -translate-x-1/2 bg-foreground/15"
            style={{ left: `${ax}%` }}
          />
          {series.map((s) => (
            <Dot key={s.label} x={ax} y={geo.y(s.values[active] ?? 0)} tone={s.tone} />
          ))}
          <div
            role="status"
            className={cn(
              "pointer-events-none absolute bottom-full z-10 mb-2 w-max min-w-28 rounded-lg bg-popover px-2.5 py-2 text-xs text-popover-foreground shadow-md ring-1 ring-foreground/10",
              align === "center" && "-translate-x-1/2",
              align === "right" && "-translate-x-full",
            )}
            style={{ left: `${ax}%` }}
          >
            <div className="text-muted-foreground">
              {formatBucket(buckets[active] ?? 0, hourly)}
              {isLast ? " (so far)" : null}
            </div>
            <ul className="mt-1 grid gap-0.5">
              {series.map((s) => (
                <li key={s.label} className="flex items-center gap-2">
                  {series.length > 1 ? (
                    <span aria-hidden className={cn("h-0.5 w-2.5 rounded-full bg-current", TONE_TEXT[s.tone])} />
                  ) : null}
                  <span className="font-semibold tabular-nums">
                    {numberFmt.format(s.values[active] ?? 0)}
                  </span>
                  <span className="text-muted-foreground">{s.label.toLowerCase()}</span>
                </li>
              ))}
            </ul>
          </div>
        </>
      ) : null}
    </div>
  );
}

function Dot({ x, y, tone }: { x: number; y: number; tone: SparkTone }) {
  return (
    <span
      aria-hidden
      className={cn(
        "pointer-events-none absolute size-2 -translate-x-1/2 -translate-y-1/2 rounded-full bg-current ring-2 ring-card",
        TONE_TEXT[tone],
      )}
      style={{ left: `${x}%`, top: `${(y / H) * 100}%` }}
    />
  );
}
