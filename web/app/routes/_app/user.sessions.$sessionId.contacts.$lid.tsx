// Contacts: drill-in (identity + direct-chat flag + shared groups with
// per-group names). Surface: contacts. Child of the contacts list — renders in its
// <Outlet/>.
//
// Ported from v1 react-router to TanStack Start:
//   - SSR: the loader seeds the core detail (qk.contact) via a Drizzle direct
//     read (contacts.server.ts, §6.2); the client hook hydrates from cache.
//   - Best-effort enrichment (picture/about) + block/unblock actions stay as
//     gateway calls via the colocated hooks (contacts.actions.ts); each degrades
//     gracefully (the row is simply hidden) on 501.
//   - useParams/useSearchParams -> Route.useParams()/Route.useSearch(); links
//     use @tanstack/react-router <Link> with typed params + preserved search.

import { createFileRoute, Link } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeftIcon,
  BanIcon,
  CircleCheckIcon,
  MessageSquareIcon,
  MoreHorizontalIcon,
  UsersIcon,
} from "lucide-react";
import { useContact } from "~/lib/api/hooks/contacts";
import { qk } from "~/lib/query";
import type { Chat, ContactDetail } from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import {
  useBlockContact,
  useContactAbout,
  useContactPicture,
} from "./-contacts-actions";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
import { Skeleton } from "~/components/ui/skeleton";
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "~/components/ui/empty";
import {
  Table,
  TableBody,
  TableCard,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "~/components/ui/table";
import { useConfirm } from "~/components/confirm-dialog";
import { CopyableId } from "~/components/copyable";
import { Time } from "~/components/time";
import { titleHead } from "~/lib/head";
import { toast } from "sonner";
import { fetchContactDetail } from "./-contacts-data";

type GroupRow = NonNullable<ContactDetail["groups"]>[number];
type Identity = NonNullable<ContactDetail["identity"]>;

interface ContactsSearch {
  q?: string;
  source?: "dm" | "group";
  group?: string;
}

function formatPhone(p: string | undefined): string | undefined {
  const d = p?.replace(/[^\d]/g, "");
  return d ? `+${d}` : undefined;
}

function displayName(c: Identity | undefined): string {
  if (!c) return "Unknown";
  return c.name || c.businessName || formatPhone(c.phoneNumber) || c.lid || "Unknown";
}

const ROLE_LABEL: Record<string, string> = {
  member: "Member",
  admin: "Admin",
  superadmin: "Owner",
};

export const Route = createFileRoute(
  "/_app/user/sessions/$sessionId/contacts/$lid",
)({
  loader: async ({ params, context }) => {
    const { sessionId, lid } = params;
    await context.queryClient.ensureQueryData({
      queryKey: qk.contact(sessionId, lid),
      queryFn: async (): Promise<ContactDetail> => {
        const detail = await fetchContactDetail({ data: { sessionId, lid } });
        return detail ?? { dm: false, groups: [] };
      },
    });
  },
  component: ContactDetailRoute,
});

function ContactDetailRoute() {
  const { sessionId, lid } = Route.useParams();
  const search = Route.useSearch();

  const detail = useContact(sessionId, lid);

  const back = (
    <Link
      to="/user/sessions/$sessionId/contacts"
      params={{ sessionId }}
      search={search}
      className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground lg:hidden"
    >
      <ArrowLeftIcon className="size-4" aria-hidden />
      All contacts
    </Link>
  );

  if (detail.isLoading) {
    return (
      <div className="grid gap-6">
        <div className="flex items-center gap-4">
          <Skeleton className="size-16 rounded-full" />
          <div className="grid gap-2">
            <Skeleton className="h-6 w-48" />
            <Skeleton className="h-4 w-32" />
          </div>
        </div>
        <Skeleton className="h-40 w-full rounded-xl" />
      </div>
    );
  }

  if (detail.isError) {
    const notFound =
      isApiError(detail.error) && detail.error.code === "not_found";
    return (
      <Empty>
        <EmptyHeader>
          <EmptyTitle>{notFound ? "Contact not found" : "Couldn't load this contact"}</EmptyTitle>
          <EmptyDescription>
            {notFound
              ? "This session hasn't seen this person, or the link is out of date."
              : isApiError(detail.error)
                ? detail.error.message
                : "Something went wrong while loading the contact."}
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent className="flex-row justify-center">
          {!notFound && (
            <Button variant="outline" onClick={() => void detail.refetch()}>
              Retry
            </Button>
          )}
          <Button asChild variant={notFound ? "outline" : "ghost"}>
            <Link to="/user/sessions/$sessionId/contacts" params={{ sessionId }} search={search}>
              Back to contacts
            </Link>
          </Button>
        </EmptyContent>
      </Empty>
    );
  }

  const data = detail.data;
  const identity = data?.identity;
  const groups = data?.groups ?? [];
  // The JID we act on (picture/about/block) — prefer the contact lid.
  const jid = identity?.lid ?? lid;

  return (
    <div className="grid gap-6">
      <div>
        {back}
        <IdentityHeader
          sessionId={sessionId}
          jid={jid}
          identity={identity}
          dm={Boolean(data?.dm)}
        />
      </div>

      <section aria-labelledby="groups-heading" className="grid gap-3">
        <h2 id="groups-heading" className="flex items-center gap-2 text-sm font-semibold">
          Shared groups
          <Badge variant="secondary" className="tabular-nums">
            {groups.length}
          </Badge>
        </h2>
        <TableCard>
          {groups.length === 0 ? (
            <Empty className="border-0 py-8">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <UsersIcon aria-hidden />
                </EmptyMedia>
                <EmptyDescription>
                  You're not in any group with this person.
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Group</TableHead>
                  <TableHead>Name in group</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>Last seen</TableHead>
                  <TableHead className="text-right">
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {groups.map((g, i) => (
                  <GroupMembershipRow
                    key={g.jid ?? i}
                    sessionId={sessionId}
                    group={g}
                  />
                ))}
              </TableBody>
            </Table>
          )}
        </TableCard>
      </section>
    </div>
  );
}

function IdentityHeader({
  sessionId,
  jid,
  identity,
  dm,
}: {
  sessionId: string;
  jid: string;
  identity: Identity | undefined;
  dm: boolean;
}) {
  // Best-effort enrichment: both may be unavailable (501) or fail while the
  // session is offline; never block the page on them, just leave them out.
  const picture = useContactPicture(sessionId, jid, Boolean(jid));
  const about = useContactAbout(sessionId, jid, Boolean(jid));
  const block = useBlockContact(sessionId);
  const confirm = useConfirm();
  const queryClient = useQueryClient();

  const name = displayName(identity);
  const phone = formatPhone(identity?.phoneNumber);
  const aboutText = about.data?.about;

  const runBlock = async (shouldBlock: boolean): Promise<void> => {
    if (shouldBlock) {
      const ok = await confirm({
        title: <>Block {name}?</>,
        description:
          "They won't be able to call or message this WhatsApp number. You can unblock them later.",
        confirmLabel: "Block",
        destructive: true,
      });
      if (!ok) return;
    }
    block.mutate(
      { jid, block: shouldBlock },
      {
        onSuccess: () =>
          toast.success(shouldBlock ? `Blocked ${name}` : `Unblocked ${name}`),
        onError: (err) => {
          if (isApiError(err) && err.isNotImplemented) {
            toast.info("Blocking isn't available on this gateway.");
            return;
          }
          toast.error(isApiError(err) ? err.message : "Action failed");
        },
      },
    );
  };

  // Same as "New chat" in Chats: seed the chat row so the thread opens
  // instantly, keyed by the contact's LID.
  const seedChat = (): void => {
    queryClient.setQueryData(qk.chat(sessionId, jid), (prev: Chat | undefined) =>
      prev ?? ({
        id: 0,
        sessionId,
        jid,
        type: "dm",
        name,
        unreadCount: 0,
        archived: false,
        pinned: false,
      } satisfies Chat),
    );
  };

  // Lay out by the pane's own width (it sits beside the contact list on
  // desktop), not the viewport: side-by-side only when there's room.
  return (
    <div className="@container">
      <div className="flex flex-col gap-4 @xl:flex-row @xl:items-start">
        <Avatar className="size-16">
          {picture.data?.url && <AvatarImage src={picture.data.url} alt={name} />}
          <AvatarFallback className="text-lg">
            {name.replace(/^\+/, "").slice(0, 2).toUpperCase()}
          </AvatarFallback>
        </Avatar>
        <div className="grid min-w-0 flex-1 gap-1.5">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <h2 className="truncate text-xl font-semibold tracking-tight">{name}</h2>
            {dm && (
              <Badge variant="secondary" className="font-normal">
                Has direct chat
              </Badge>
            )}
          </div>
          {identity?.businessName && identity.businessName !== name && (
            <p className="text-sm text-muted-foreground">{identity.businessName}</p>
          )}
          {aboutText && (
            <p className="text-sm text-pretty text-muted-foreground">“{aboutText}”</p>
          )}
          <dl className="mt-1 flex flex-wrap gap-x-6 gap-y-2 text-sm">
            {phone && (
              <div className="grid gap-0.5">
                <dt className="text-xs text-muted-foreground">Phone</dt>
                <dd>
                  <CopyableId
                    value={phone}
                    label="Copy phone number"
                    className="[&>span:first-child]:font-sans [&>span:first-child]:text-sm"
                  />
                </dd>
              </div>
            )}
            {identity?.lid && (
              <div className="grid min-w-0 gap-0.5">
                <dt className="text-xs text-muted-foreground">LID</dt>
                <dd className="min-w-0">
                  <CopyableId value={identity.lid} label="Copy LID" />
                </dd>
              </div>
            )}
          </dl>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          <Button asChild>
            <Link
              to="/user/sessions/$sessionId/chats/$chatId"
              params={{ sessionId, chatId: jid }}
              preload={false}
              onClick={seedChat}
            >
              <MessageSquareIcon aria-hidden data-icon="inline-start" />
              {dm ? "Open chat" : "Message"}
            </Link>
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                disabled={block.isPending}
                aria-label={`More actions for ${name}`}
              >
                <MoreHorizontalIcon aria-hidden />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-40">
              <DropdownMenuItem onSelect={() => void runBlock(false)}>
                <CircleCheckIcon aria-hidden />
                Unblock
              </DropdownMenuItem>
              <DropdownMenuItem variant="destructive" onSelect={() => void runBlock(true)}>
                <BanIcon aria-hidden />
                Block…
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
    </div>
  );
}

function GroupMembershipRow({
  sessionId,
  group,
}: {
  sessionId: string;
  group: GroupRow;
}) {
  const groupName = group.name || "Unnamed group";
  return (
    <TableRow>
      <TableCell className="max-w-[16rem]">
        <span className="block truncate font-medium">{groupName}</span>
        {group.jid && (
          <CopyableId
            value={group.jid}
            truncate={[10, 12]}
            label="Copy group JID"
            className="text-muted-foreground"
          />
        )}
      </TableCell>
      <TableCell>{group.tag || <span className="text-muted-foreground">—</span>}</TableCell>
      <TableCell>
        {group.role ? (
          <Badge variant="outline" className="font-normal">
            {ROLE_LABEL[group.role] ?? group.role}
          </Badge>
        ) : (
          "—"
        )}
      </TableCell>
      <TableCell className="text-muted-foreground">
        <Time value={group.lastSeen} />
      </TableCell>
      <TableCell className="text-right">
        {group.jid && (
          <Button asChild size="sm" variant="ghost">
            <Link
              to="/user/sessions/$sessionId/contacts"
              params={{ sessionId }}
              search={{ source: "group", group: group.jid }}
            >
              Show members
            </Link>
          </Button>
        )}
      </TableCell>
    </TableRow>
  );
}
