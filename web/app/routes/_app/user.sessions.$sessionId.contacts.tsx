// Contacts: the people this session has seen (in direct chats or groups),
// searchable, with a "seen in" filter. Surface: contacts. Master pane of the master/detail layout — the drill-in renders in
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
import { useGroup } from "~/lib/api/hooks/groups";
import { useSession } from "~/lib/api/hooks/sessions";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Badge } from "~/components/ui/badge";
import { Skeleton } from "~/components/ui/skeleton";
import { ScrollArea } from "~/components/ui/scroll-area";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "~/components/ui/empty";
import { useFillMain } from "~/components/shell/page-chrome";
import { titleHead } from "~/lib/head";
import { middleTruncate } from "~/lib/format";
import { cn } from "~/lib/utils";
import { ContactRoundIcon, SearchIcon, UsersIcon, XIcon } from "lucide-react";
import { ChatAvatar } from "./user/sessions/$sessionId/chats/-viewer-ui";
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
  return c.name || c.businessName || formatPhone(c.phoneNumber) || c.lid || "Unknown";
}

/** Digits → "+<digits>", matching how session phone numbers are shown. */
function formatPhone(p: string | undefined): string | undefined {
  const d = p?.replace(/[^\d]/g, "");
  return d ? `+${d}` : undefined;
}

const SOURCE_LABEL: Record<Contact["source"], string> = {
  dm: "Direct",
  group: "Group",
};

export const Route = createFileRoute(
  "/_app/user/sessions/$sessionId/contacts",
)({
  validateSearch,
  head: () => titleHead("Contacts"),
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
  // Master/detail surface owns its own scrolling — clamp <main> to the viewport
  // (the session-detail layout owns the top-bar back/tabs).
  useFillMain();
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
  const clearFilters = (): void => {
    setSearchText("");
    void navigate({ search: {}, replace: true });
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

  // Below `lg` there's room for one pane: the list, or the open contact.
  return (
    <div className="grid h-full min-h-0 lg:grid-cols-[22rem_minmax(0,1fr)] lg:grid-rows-[minmax(0,1fr)]">
      <section
        aria-label="Contacts"
        className={cn(
          "min-h-0 flex-col overflow-hidden border-border/70 lg:flex lg:border-r",
          lid ? "hidden" : "flex",
        )}
      >
        <header className="grid gap-2.5 border-b border-border/70 p-3">
          <div className="flex h-7 items-center justify-between gap-2">
            <h1 className="text-sm font-semibold">Contacts</h1>
            {hasFilters && (
              <Button
                variant="ghost"
                size="xs"
                className="text-muted-foreground"
                onClick={clearFilters}
              >
                Clear filters
              </Button>
            )}
          </div>
          <div className="relative">
            <SearchIcon
              className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
              aria-hidden
            />
            <Input
              type="search"
              placeholder="Search name or phone"
              aria-label="Search contacts"
              value={searchText}
              onChange={(e) => setSearchText(e.target.value)}
              className="pl-8"
            />
          </div>
          <div className="flex items-center gap-2">
            <span className="shrink-0 text-xs text-muted-foreground" id="seen-in-label">
              Seen in
            </span>
            <Select
              value={sourceValue}
              onValueChange={(v) => setSource(v as SourceOpt)}
            >
              <SelectTrigger className="h-8 w-full" aria-labelledby="seen-in-label">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All</SelectItem>
                <SelectItem value="dm">Direct chats</SelectItem>
                <SelectItem value="group">Groups</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {filter.group && (
            <GroupChip
              sessionId={sessionId}
              groupJid={filter.group}
              onClear={clearGroup}
            />
          )}
        </header>

        <ScrollArea className="min-h-0 flex-1">
          <ContactRows
            sessionId={sessionId}
            rows={rows}
            activeLid={lid}
            search={search}
            list={contacts}
            hasFilters={hasFilters}
            onClearFilters={clearFilters}
          />
        </ScrollArea>

        {contacts.hasNextPage && (
          <div className="border-t border-border/70 p-2">
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

      <section
        aria-label="Contact detail"
        className={cn(
          "min-h-0 min-w-0 overflow-y-auto lg:block",
          lid ? "block" : "hidden",
        )}
      >
        {lid ? (
          <div className="mx-auto w-full max-w-4xl p-4 sm:p-5 xl:p-6">
            <Outlet />
          </div>
        ) : (
          <Empty className="h-full rounded-none border-0">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <ContactRoundIcon aria-hidden />
              </EmptyMedia>
              <EmptyTitle>Select a contact</EmptyTitle>
              <EmptyDescription>
                See their details, the groups you share, and start a chat.
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        )}
      </section>
    </div>
  );
}

/** The active "in group" filter, with the group's name when we can resolve it. */
function GroupChip({
  sessionId,
  groupJid,
  onClear,
}: {
  sessionId: string;
  groupJid: string;
  onClear: () => void;
}) {
  const group = useGroup(sessionId, groupJid);
  const name = group.data?.subject || middleTruncate(groupJid, 10, 12);
  return (
    <div className="flex min-w-0 items-center gap-1.5 self-start rounded-full bg-muted py-0.5 pr-0.5 pl-2.5 text-xs">
      <UsersIcon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
      <span className="min-w-0 truncate" title={groupJid}>
        In <span className="font-medium">{name}</span>
      </span>
      <Button
        variant="ghost"
        size="icon-xs"
        className="rounded-full text-muted-foreground"
        onClick={onClear}
        aria-label="Remove group filter"
      >
        <XIcon aria-hidden />
      </Button>
    </div>
  );
}

function ContactRows({
  sessionId,
  rows,
  activeLid,
  search,
  list,
  hasFilters,
  onClearFilters,
}: {
  sessionId: string;
  rows: Contact[];
  activeLid: string | undefined;
  search: ContactsSearch;
  list: ReturnType<typeof useContacts>;
  hasFilters: boolean;
  onClearFilters: () => void;
}) {
  const session = useSession(sessionId);

  if (list.isLoading) {
    return (
      <div className="grid gap-1 p-2">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-12 w-full rounded-lg" />
        ))}
      </div>
    );
  }

  if (list.isError) {
    const msg = isApiError(list.error)
      ? list.error.message
      : "Failed to load contacts";
    return (
      <div className="grid justify-items-start gap-2 p-4">
        <p className="text-sm text-destructive">{msg}</p>
        <Button variant="outline" size="sm" onClick={() => void list.refetch()}>
          Retry
        </Button>
      </div>
    );
  }

  if (rows.length === 0) {
    const unpaired = session.data && !session.data.waJid;
    return (
      <Empty className="border-0 px-4 py-10">
        <EmptyHeader>
          <EmptyTitle className="text-sm">
            {hasFilters ? "No contacts match" : "No contacts yet"}
          </EmptyTitle>
          <EmptyDescription className="text-xs">
            {hasFilters
              ? "Try a different search, or clear the filters."
              : unpaired
                ? "Link a phone to this session first. Contacts appear as it sees messages."
                : "Contacts appear as this session sees messages. Import a backup from the Overview to load more."}
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          {hasFilters ? (
            <Button size="sm" variant="outline" onClick={onClearFilters}>
              Clear filters
            </Button>
          ) : (
            <Button asChild size="sm" variant="outline">
              <Link to="/user/sessions/$sessionId" params={{ sessionId }}>
                {unpaired ? "Link phone" : "Go to Overview"}
              </Link>
            </Button>
          )}
        </EmptyContent>
      </Empty>
    );
  }

  return (
    <ul className="grid gap-0.5 p-1.5">
      {rows.map((c) => {
        const id = c.lid ?? "";
        const active = activeLid === id;
        const name = displayName(c);
        const phone = formatPhone(c.phoneNumber);
        return (
          <li key={id || name}>
            <Link
              to="/user/sessions/$sessionId/contacts/$lid"
              params={{ sessionId, lid: id }}
              search={search}
              aria-current={active ? "true" : undefined}
              className={cn(
                "flex items-center gap-3 rounded-lg px-2.5 py-2 text-sm transition-colors duration-150 ease-soft hover:bg-muted/70",
                active && "bg-muted",
              )}
            >
              <ChatAvatar
                sessionId={sessionId}
                name={c.name || c.businessName || undefined}
                jid={id || undefined}
                type="dm"
              />
              <div className="min-w-0 flex-1">
                <p className="truncate font-medium">{name}</p>
                {phone && phone !== name && (
                  <p className="truncate text-xs text-muted-foreground tabular-nums">
                    {phone}
                  </p>
                )}
              </div>
              {c.source && (
                <Badge variant="secondary" className="shrink-0 font-normal">
                  {SOURCE_LABEL[c.source] ?? c.source}
                </Badge>
              )}
            </Link>
          </li>
        );
      })}
    </ul>
  );
}
