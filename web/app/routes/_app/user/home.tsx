// User: workspace Home. The landing page for everyone with the user panel:
// a "Get started" checklist (connect a number, create a key, send a message,
// add a webhook) and an activity grid for the active organization over a
// selectable range (?range=24h|7d|14d|30d, default 7d).
//
// Data: GET /overview (totals + per-bucket series), GET /sessions (Sessions
// tile + step 1), better-auth apiKey.list (step 2), GET /webhooks (step 4).
// An API without /overview (404) still gets the checklist and Sessions tile.

import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { BuildingIcon, InfoIcon, ListChecksIcon } from "lucide-react";
import { Page, PageTitle } from "~/components/shell/page-title";
import { Button } from "~/components/ui/button";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "~/components/ui/empty";
import { isApiError } from "~/lib/api/envelope";
import {
  OVERVIEW_RANGE_DAYS,
  OVERVIEW_RANGES,
  useOverview,
  type OverviewRange,
} from "~/lib/api/hooks/overview";
import { useSessions } from "~/lib/api/hooks/sessions";
import { useWebhooks } from "~/lib/api/hooks/webhooks";
import { authClient } from "~/lib/auth/client";
import { useAppSession } from "~/lib/auth/context";
import { titleHead } from "~/lib/head";
import { cn } from "~/lib/utils";
import { GetStarted, useChecklistPref } from "./-home/get-started";
import { MetricGrid, OverviewTiles, OverviewUnavailable, SessionsTile } from "./-home/metrics";

interface HomeSearch {
  range?: OverviewRange;
}

function validateSearch(search: Record<string, unknown>): HomeSearch {
  const r = search.range;
  return {
    range:
      typeof r === "string" && (OVERVIEW_RANGES as readonly string[]).includes(r)
        ? (r as OverviewRange)
        : undefined,
  };
}

export const Route = createFileRoute("/_app/user/home")({
  validateSearch,
  head: () => titleHead("Home"),
  component: Home,
});

const DEFAULT_RANGE: OverviewRange = "7d";

const RANGE_LABEL: Record<OverviewRange, string> = {
  "24h": "Last 24 hours",
  "7d": "Last 7 days",
  "14d": "Last 14 days",
  "30d": "Last 30 days",
};

function Home() {
  const session = useAppSession();
  const orgId = session.activeOrg?.id;

  if (!orgId) {
    return (
      <Page>
        <PageTitle title="Home" />
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <BuildingIcon aria-hidden />
            </EmptyMedia>
            <EmptyTitle>No organization selected</EmptyTitle>
            <EmptyDescription>
              Pick an organization from the switcher at the top to see its activity.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      </Page>
    );
  }
  return <Workspace orgId={orgId} />;
}

function Workspace({ orgId }: { orgId: string }) {
  const range = Route.useSearch().range ?? DEFAULT_RANGE;
  const navigate = Route.useNavigate();

  const overview = useOverview({ range });
  const sessions = useSessions();
  const webhooks = useWebhooks();
  // Same query key and shape as the API keys page, so the two share a cache.
  const keys = useQuery({
    queryKey: ["auth", "api-keys", orgId] as const,
    queryFn: async (): Promise<unknown[]> => {
      const { data, error } = await authClient.apiKey.list({
        query: { organizationId: orgId },
      });
      if (error) throw new Error(error.message ?? "Failed to load keys");
      const rows = (data as { apiKeys?: unknown[] } | null)?.apiKeys;
      return Array.isArray(rows) ? rows : [];
    },
  });

  const [pref, setPref] = useChecklistPref(orgId);

  const sessionList = sessions.data?.pages.flatMap((p) => p.data ?? []) ?? [];
  const firstWorking = sessionList.find((s) => s.status === "working");
  const webhookCount = webhooks.data?.pages.reduce((n, p) => n + (p.data?.length ?? 0), 0);

  // A failed source leaves its step unchecked rather than spinning forever.
  const checklist = {
    connected: sessions.isPending ? undefined : Boolean(firstWorking),
    hasKey: keys.isPending ? undefined : (keys.data?.length ?? 0) > 0,
    hasSent: overview.isPending ? undefined : Boolean(overview.data?.hasSentMessage),
    hasWebhook: webhooks.isPending ? undefined : (webhookCount ?? 0) > 0,
  };

  const missing = isApiError(overview.error) && overview.error.status === 404;
  const retention = overview.data?.retentionDays ?? 0;
  const beyondRetention = retention > 0 && OVERVIEW_RANGE_DAYS[range] > retention;

  const setRange = (next: OverviewRange) =>
    void navigate({
      search: (prev) => ({ ...prev, range: next === DEFAULT_RANGE ? undefined : next }),
      replace: true,
    });

  return (
    <Page>
      <PageTitle
        title="Home"
        description="Message activity across your WhatsApp numbers, and what's left to set up."
        actions={
          pref === "dismissed" ? (
            <Button variant="outline" onClick={() => setPref(null)}>
              <ListChecksIcon aria-hidden data-icon="inline-start" />
              Setup checklist
            </Button>
          ) : null
        }
      />

      <GetStarted state={checklist} firstWorking={firstWorking} pref={pref} onPref={setPref} />

      <section aria-labelledby="activity-title" className="grid gap-3">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
          <div className="grid gap-0.5">
            <h2 id="activity-title" className="text-base font-semibold tracking-tight">
              Activity
            </h2>
            <p className="text-sm text-muted-foreground">
              {missing ? "Your WhatsApp numbers right now." : `${RANGE_LABEL[range]}, in your local time.`}
            </p>
          </div>
          {missing ? null : <RangeControl value={range} onChange={setRange} />}
        </div>

        {beyondRetention ? (
          <p className="flex items-start gap-2 rounded-lg bg-muted/50 px-3 py-2 text-xs text-muted-foreground">
            <InfoIcon aria-hidden className="mt-px size-3.5 shrink-0" />
            Messages are kept for {retention} {retention === 1 ? "day" : "days"} on this gateway, so
            days before that show as zero.
          </p>
        ) : null}

        <MetricGrid busy={overview.isPlaceholderData}>
          {overview.isError ? (
            <OverviewUnavailable missing={missing} onRetry={() => void overview.refetch()} />
          ) : (
            <OverviewTiles data={overview.data} />
          )}
          <SessionsTile
            sessions={sessionList}
            more={Boolean(sessions.hasNextPage)}
            loading={sessions.isPending}
          />
        </MetricGrid>
      </section>
    </Page>
  );
}

/** 24h / 7d / 14d / 30d segmented control (a radio group). */
function RangeControl({
  value,
  onChange,
}: {
  value: OverviewRange;
  onChange: (next: OverviewRange) => void;
}) {
  const move = (group: HTMLElement, dir: 1 | -1) => {
    const i = (OVERVIEW_RANGES.indexOf(value) + dir + OVERVIEW_RANGES.length) % OVERVIEW_RANGES.length;
    const next = OVERVIEW_RANGES[i];
    if (!next) return;
    onChange(next);
    group.querySelectorAll<HTMLButtonElement>('[role="radio"]')[i]?.focus();
  };
  return (
    <div
      role="radiogroup"
      aria-label="Time range"
      onKeyDown={(e) => {
        if (e.key === "ArrowRight" || e.key === "ArrowDown") {
          e.preventDefault();
          move(e.currentTarget, 1);
        } else if (e.key === "ArrowLeft" || e.key === "ArrowUp") {
          e.preventDefault();
          move(e.currentTarget, -1);
        }
      }}
      className="inline-flex w-fit items-center gap-0.5 rounded-lg bg-muted p-[3px]"
    >
      {OVERVIEW_RANGES.map((r) => {
        const checked = r === value;
        return (
          <button
            key={r}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={RANGE_LABEL[r]}
            tabIndex={checked ? 0 : -1}
            onClick={() => onChange(r)}
            className={cn(
              "h-7 min-w-11 rounded-md px-2.5 text-xs font-medium tabular-nums transition-all duration-200 ease-soft outline-none focus-visible:ring-3 focus-visible:ring-ring/30",
              checked
                ? "bg-background text-foreground shadow-xs dark:bg-input/50"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {r}
          </button>
        );
      })}
    </div>
  );
}
