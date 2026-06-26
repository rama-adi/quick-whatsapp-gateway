// Contact ("found users") hooks: filtered list + drill-in detail.
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
import type { Contact, ContactDetail, ContactFilter } from "../types";
import { apiUrl, fetchJSON, listPageFetcher, nextCursor } from "./_shared";

export function useContacts(
  s: string,
  f: ContactFilter,
): UseInfiniteQueryResult<
  InfiniteData<Page<Contact>, string | undefined>,
  ApiError
> {
  return useInfiniteQuery({
    queryKey: qk.contacts(s, f),
    enabled: Boolean(s),
    initialPageParam: undefined as string | undefined,
    queryFn: listPageFetcher<Contact>(`/sessions/${encodeURIComponent(s)}/contacts`, {
      source: f.source,
      group: f.group,
      q: f.q,
    }),
    getNextPageParam: nextCursor,
  });
}

export function useContact(
  s: string,
  lid: string,
): UseQueryResult<ContactDetail, ApiError> {
  return useQuery({
    queryKey: qk.contact(s, lid),
    enabled: Boolean(s && lid),
    queryFn: () =>
      fetchJSON<ContactDetail>(
        apiUrl(`/sessions/${encodeURIComponent(s)}/contacts/${encodeURIComponent(lid)}`),
      ),
  });
}
