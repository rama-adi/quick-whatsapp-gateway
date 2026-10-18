// Workspace activity summary for the Home page: GET /overview.
//
// Totals and zero-filled per-bucket series for one time range, plus the
// "has this org ever sent a message" flag the get-started checklist reads.
// Session counts, API keys and webhook configs are NOT here; Home reads those
// from their own hooks.

import {
  keepPreviousData,
  useQuery,
  type UseQueryResult,
} from "@tanstack/react-query";
import { useSyncExternalStore } from "react";
import { qk } from "../../query";
import type { ApiError } from "../envelope";
import type { components } from "../schema";
import { apiUrl, fetchJSON, queryString } from "./_shared";

/** Response of GET /overview (epoch-ms timestamps, like the rest of the API). */
export type Overview = components["schemas"]["Overview"];
export type OverviewTotals = components["schemas"]["OverviewTotals"];
export type OverviewSeries = components["schemas"]["OverviewSeries"];

/** The window lengths the endpoint accepts. 24h = hourly buckets, else daily. */
export type OverviewRange = Overview["range"];
export const OVERVIEW_RANGES = ["24h", "7d", "14d", "30d"] as const satisfies readonly OverviewRange[];

/** Days covered by each range (24h counts as 1). */
export const OVERVIEW_RANGE_DAYS: Record<OverviewRange, number> = {
  "24h": 1,
  "7d": 7,
  "14d": 14,
  "30d": 30,
};

export interface OverviewParams {
  range: OverviewRange;
  /** Limit to one session; omit for every session in the active org. */
  session?: string;
}

const noopSubscribe = () => () => {};

/** The browser's IANA time zone (UTC during SSR), hydration-safe. */
function useTimeZone(): string {
  return useSyncExternalStore(
    noopSubscribe,
    () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
    () => "UTC",
  );
}

/**
 * Activity for the active org over `range`, with daily buckets aligned to the
 * browser's local midnight. Keeps the previous range's data on screen while a
 * new range loads.
 */
export function useOverview({
  range,
  session,
}: OverviewParams): UseQueryResult<Overview, ApiError> {
  const tz = useTimeZone();
  return useQuery({
    queryKey: qk.overview({ range, tz, session }),
    queryFn: ({ signal }) =>
      fetchJSON<Overview>(apiUrl(`/overview${queryString({ range, tz, session })}`), {
        signal,
      }),
    placeholderData: keepPreviousData,
    staleTime: 60_000,
  });
}
