// Group hooks (list/get + members).
// FROZEN — owned by the foundation agent.

import {
  useInfiniteQuery,
  useQuery,
  type InfiniteData,
  type UseInfiniteQueryResult,
  type UseQueryResult,
} from "@tanstack/react-query";
import { qk } from "../../query";
import type { ApiError, Page } from "../envelope";
import type { Group, GroupMember } from "../types";
import { apiUrl, fetchJSON, listPageFetcher, nextCursor } from "./_shared";

export function useGroups(
  s: string,
): UseInfiniteQueryResult<InfiniteData<Page<Group>, string | undefined>, ApiError> {
  return useInfiniteQuery({
    queryKey: qk.groups(s),
    enabled: Boolean(s),
    initialPageParam: undefined as string | undefined,
    queryFn: listPageFetcher<Group>(`/sessions/${encodeURIComponent(s)}/groups`),
    getNextPageParam: nextCursor,
  });
}

export function useGroup(s: string, gid: string): UseQueryResult<Group, ApiError> {
  return useQuery({
    queryKey: qk.group(s, gid),
    enabled: Boolean(s && gid),
    queryFn: () =>
      fetchJSON<Group>(
        apiUrl(`/sessions/${encodeURIComponent(s)}/groups/${encodeURIComponent(gid)}`),
      ),
  });
}

export function useGroupMembers(
  s: string,
  gid: string,
): UseQueryResult<GroupMember[], ApiError> {
  return useQuery({
    queryKey: [...qk.group(s, gid), "members"],
    enabled: Boolean(s && gid),
    queryFn: () =>
      fetchJSON<GroupMember[]>(
        apiUrl(
          `/sessions/${encodeURIComponent(s)}/groups/${encodeURIComponent(gid)}/members`,
        ),
      ),
  });
}
