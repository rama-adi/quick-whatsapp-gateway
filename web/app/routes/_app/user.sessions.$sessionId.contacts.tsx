// Contacts ("found users"): searchable list with where-found filters. Surface:
// contacts. Master pane of the master/detail layout — the drill-in renders in
// the nested <Outlet/>.
//
// Ported from v1 react-router to TanStack Start:
//   - Filters (?q=&source=&group=) move from useSearchParams to the route's
//     typed `validateSearch`; they feed the query key so the cache, back/forward
//     and shareable links stay in sync (same behaviour as v1).
//   - SSR: the loader seeds page 0 of the filtered list under qk.contacts(s,f)
//     via a Drizzle direct read (contacts.server.ts, §6.2).
//   - Realtime: contact.update invalidates the contacts root in the cacheBridge.
//   - Link/Outlet from @tanstack/react-router; navigate() replaces setSearchParams.

import { useEffect, useMemo, useRef, useState } from "react";
import {
  createFileRoute,
  Link,
  Outlet,
  useNavigate,
  useParams,
} from "@tanstack/react-router";
import type { InfiniteData } from "@tanstack/react-query";
import { useContacts } from "~/lib/api/hooks/contacts";
import { qk } from "~/lib/query";
import type { Contact, ContactFilter } from "~/lib/api/types";
import type { Page } from "~/lib/api/envelope";
import { isApiError } from "~/lib/api/envelope";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Badge } from "~/components/ui/badge";
import { Skeleton } from "~/components/ui/skeleton";
import { Avatar, AvatarFallback } from "~/components/ui/avatar";
import { ScrollArea } from "~/components/ui/scroll-area";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "~/components/ui/select";
import { fetchContactsPage } from "./-contacts-data";

type SourceOpt = "all" | "dm" | "group";

interface ContactsSearch {
  q?: string;
  source?: "dm" | "group";
  group?: string;
}

/** Normalize raw URL search into the typed filter the surface uses. */
function validateSearch(search: Record<string, unknown>): ContactsSearch {
  const source = search.source;
  return {
    q: typeof search.q === "string" && search.q ? search.q : undefined,
    source: source === "dm" || source === "group" ? source : undefined,
    group:
      typeof search.group === "string" && search.group
        ? search.group
        : undefined,
  };
}

function toFilter(s: ContactsSearch): ContactFilter {
  return { q: s.q, source: s.source, group: s.group };
}

function displayName(c: Contact): string {
  return c.name || c.businessName || c.phoneNumber || c.lid || "Unknown";
}

function initials(c: Contact): string {
  return displayName(c).slice(0, 2).toUpperCase();
}

export const Route = createFileRoute(
  "/_app/user/sessions/$sessionId/contacts",
)({
  validateSearch,
  // Re-seed when the filter changes so back/forward + shareable URLs hydrate.
  loaderDeps: ({ search }) => ({
    q: search.q,
    source: search.source,
    group: search.group,
  }),
  loader: async ({ params, deps, context }) => {
    const filter = toFilter(deps);
    await context.queryClient.ensureInfiniteQueryData({
      queryKey: qk.contacts(params.sessionId, filter),
      initialPageParam: undefined as string | undefined,
      queryFn: () =>
        fetchContactsPage({ data: { sessionId: params.sessionId, filter } }),
      getNextPageParam: (last: Page<Contact>) => last.nextCursor ?? undefined,
    });
  },
  component: ContactsList,
});

function ContactsList() {
  const { sessionId } = Route.useParams();
  // The active child param (lid) lives on the nested detail route; read merged
  // params non-strictly to highlight the selected row.
  const { lid } = useParams({ strict: false });
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });

  const filter = useMemo(() => toFilter(search), [search]);

  // Local search box state, debounced into the URL so we don't refetch on every
  // keystroke while keeping the query key (and shareable URL) authoritative.
  const [searchText, setSearchText] = useState(filter.q ?? "");
  useEffect(() => {
    setSearchText(filter.q ?? "");
  }, [filter.q]);

  const firstRun = useRef(true);
  useEffect(() => {
    if (firstRun.current) {
      firstRun.current = false;
      return;
    }
    const handle = setTimeout(() => {
      void navigate({
        search: (prev) => ({ ...prev, q: searchText || undefined }),
        replace: true,
      });
    }, 300);
    return () => clearTimeout(handle);
  }, [searchText, navigate]);

  const setSource = (value: SourceOpt): void => {
    void navigate({
      search: (prev) => {
        if (value === "all") {
          return { ...prev, source: undefined, group: undefined };
        }
        if (value === "dm") {
          return { ...prev, source: "dm", group: undefined };
        }
        return { ...prev, source: "group" };
      },
    });
  };

  const clearGroup = (): void => {
    void navigate({ search: (prev) => ({ ...prev, group: undefined }) });
  };

  const contacts = useContacts(sessionId, filter);
  const rows: Contact[] = useMemo(
    () =>
      (contacts.data as InfiniteData<Page<Contact>> | undefined)?.pages.flatMap(
        (p) => p.data,
      ) ?? [],
    [contacts.data],
  );

  const sourceValue: SourceOpt = filter.source ?? "all";
  const hasFilters = Boolean(filter.q || filter.source || filter.group);

  return (
    <div className="grid gap-4 lg:grid-cols-[340px_1fr]">
      <section
        aria-label="Found users"
        className="flex max-h-[calc(100vh-9rem)] flex-col rounded-lg border bg-card"
      >
        <header className="space-y-3 border-b p-3">
          <div className="flex items-center justify-between">
            <h1 className="text-sm font-semibold">Found users</h1>
            {hasFilters && (
              <Button
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs"
                onClick={() => void navigate({ search: {}, replace: true })}
              >
                Clear
              </Button>
            )}
          </div>
          <Input
            type="search"
            placeholder="Search name, phone, push name…"
            aria-label="Search contacts"
            value={searchText}
            onChange={(e) => setSearchText(e.target.value)}
          />
          <Select
            value={sourceValue}
            onValueChange={(v) => setSource(v as SourceOpt)}
          >
            <SelectTrigger className="h-8 w-full" aria-label="Where found">
              <SelectValue placeholder="Where found" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">Anywhere</SelectItem>
              <SelectItem value="dm">Direct messages</SelectItem>
              <SelectItem value="group">Groups</SelectItem>
            </SelectContent>
          </Select>
          {filter.group && (
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <span className="min-w-0 flex-1 truncate">
                In group: <span className="font-mono">{filter.group}</span>
              </span>
              <button
                type="button"
                className="shrink-0 text-primary hover:underline"
                onClick={clearGroup}
              >
                remove
              </button>
            </div>
          )}
        </header>

        <ScrollArea className="flex-1">
          <ContactRows
            sessionId={sessionId}
            rows={rows}
            activeLid={lid}
            search={search}
            list={contacts}
          />
        </ScrollArea>

        {contacts.hasNextPage && (
          <div className="border-t p-2">
            <Button
              variant="outline"
              size="sm"
              className="w-full"
              disabled={contacts.isFetchingNextPage}
              onClick={() => void contacts.fetchNextPage()}
            >
              {contacts.isFetchingNextPage ? "Loading…" : "Load more"}
            </Button>
          </div>
        )}
      </section>

      <section aria-label="Contact detail" className="min-w-0">
        <Outlet />
      </section>
    </div>
  );
}

function ContactRows({
  sessionId,
  rows,
  activeLid,
  search,
  list,
}: {
  sessionId: string;
  rows: Contact[];
  activeLid: string | undefined;
  search: ContactsSearch;
  list: ReturnType<typeof useContacts>;
}) {
  if (list.isLoading) {
    return (
      <div className="space-y-2 p-3">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-12 w-full" />
        ))}
      </div>
    );
  }

  if (list.isError) {
    const msg = isApiError(list.error)
      ? list.error.message
      : "Failed to load contacts";
    return (
      <div className="p-4">
        <p className="text-sm text-destructive">{msg}</p>
        <Button
          variant="outline"
          size="sm"
          className="mt-2"
          onClick={() => void list.refetch()}
        >
          Retry
        </Button>
      </div>
    );
  }

  if (rows.length === 0) {
    return (
      <p className="p-6 text-center text-sm text-muted-foreground">
        No contacts found.
      </p>
    );
  }

  return (
    <ul className="divide-y">
      {rows.map((c) => {
        const id = c.lid ?? "";
        const active = activeLid === id;
        return (
          <li key={id || displayName(c)}>
            <Link
              to="/user/sessions/$sessionId/contacts/$lid"
              params={{ sessionId, lid: id }}
              search={search}
              aria-current={active ? "true" : undefined}
              className={`flex items-center gap-3 px-3 py-2.5 text-sm transition-colors hover:bg-accent ${
                active ? "bg-accent" : ""
              }`}
            >
              <Avatar className="size-9">
                <AvatarFallback>{initials(c)}</AvatarFallback>
              </Avatar>
              <div className="min-w-0 flex-1">
                <p className="truncate font-medium">{displayName(c)}</p>
                {c.phoneNumber && (
                  <p className="truncate text-xs text-muted-foreground">
                    {c.phoneNumber}
                  </p>
                )}
              </div>
              {c.source && (
                <Badge variant="secondary" className="shrink-0 capitalize">
                  {c.source}
                </Badge>
              )}
            </Link>
          </li>
        );
      })}
    </ul>
  );
}
