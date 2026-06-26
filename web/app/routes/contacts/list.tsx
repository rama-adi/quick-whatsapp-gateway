// Contacts ("found users"): searchable list with where-found filters. Surface
// agent: contacts. Master pane of the master/detail layout — the drill-in renders
// in <Outlet/>. Filters (?q=&source=&group=) live in the URL and feed the query
// key, so the cache, back/forward, and shareable links all stay in sync.

import { useEffect, useMemo, useRef, useState } from "react";
import {
  Link,
  Outlet,
  useParams,
  useSearchParams,
  useLocation,
} from "react-router";
import { useContacts } from "~/lib/api/hooks/contacts";
import type { Contact, ContactFilter } from "~/lib/api/types";
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

type SourceOpt = "all" | "dm" | "group";

/** Read the contact filter out of the URL search params. */
function filterFromParams(params: URLSearchParams): ContactFilter {
  const source = params.get("source");
  return {
    q: params.get("q") || undefined,
    source: source === "dm" || source === "group" ? source : undefined,
    group: params.get("group") || undefined,
  };
}

/** Preferred display label: push name wins, then saved name, then phone/lid. */
function displayName(c: Contact): string {
  return c.pushName || c.name || c.phoneNumber || c.lid || "Unknown";
}

function initials(c: Contact): string {
  return displayName(c).slice(0, 2).toUpperCase();
}

export default function ContactsList() {
  const { sessionId, lid } = useParams();
  const session = sessionId ?? "";
  const location = useLocation();
  const [params, setParams] = useSearchParams();

  const filter = useMemo(() => filterFromParams(params), [params]);

  // Local search box state, debounced into the URL so we don't refetch on every
  // keystroke while keeping the query key (and shareable URL) authoritative.
  const [search, setSearch] = useState(filter.q ?? "");
  useEffect(() => {
    setSearch(filter.q ?? "");
  }, [filter.q]);

  const firstRun = useRef(true);
  useEffect(() => {
    if (firstRun.current) {
      firstRun.current = false;
      return;
    }
    const handle = setTimeout(() => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (search) next.set("q", search);
          else next.delete("q");
          return next;
        },
        { replace: true },
      );
    }, 300);
    return () => clearTimeout(handle);
  }, [search, setParams]);

  const setSource = (value: SourceOpt): void => {
    setParams((prev) => {
      const next = new URLSearchParams(prev);
      if (value === "all") {
        next.delete("source");
        next.delete("group"); // group filter only meaningful with source=group
      } else {
        next.set("source", value);
        if (value === "dm") next.delete("group");
      }
      return next;
    });
  };

  const clearGroup = (): void => {
    setParams((prev) => {
      const next = new URLSearchParams(prev);
      next.delete("group");
      return next;
    });
  };

  const contacts = useContacts(session, filter);
  const rows: Contact[] = useMemo(
    () => contacts.data?.pages.flatMap((p) => p.data) ?? [],
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
                onClick={() => setParams({}, { replace: true })}
              >
                Clear
              </Button>
            )}
          </div>
          <Input
            type="search"
            placeholder="Search name, phone, push name…"
            aria-label="Search contacts"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
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
            session={session}
            rows={rows}
            activeLid={lid}
            search={location.search}
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
  session,
  rows,
  activeLid,
  search,
  list,
}: {
  session: string;
  rows: Contact[];
  activeLid: string | undefined;
  search: string;
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
              to={{
                pathname: `/user/sessions/${encodeURIComponent(session)}/contacts/${encodeURIComponent(id)}`,
                search,
              }}
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
