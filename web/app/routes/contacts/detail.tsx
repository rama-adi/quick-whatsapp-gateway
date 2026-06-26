// Contacts: drill-in (identity + dm flag + group memberships). Surface agent:
// contacts. Reads useContact() for the core detail, then layers optional
// best-effort calls (on-WhatsApp check, picture, about) and block/unblock
// actions — each degrades gracefully to "not available in v1" on 501.

import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { useContact } from "~/lib/api/hooks/contacts";
import type { Contact, ContactDetail } from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import {
  useBlockContact,
  useContactAbout,
  useContactPicture,
} from "./api";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
import { Skeleton } from "~/components/ui/skeleton";
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "~/components/ui/table";
import { toast } from "sonner";

type GroupRow = NonNullable<ContactDetail["groups"]>[number];

function displayName(c: Contact | undefined): string {
  if (!c) return "Unknown";
  return c.pushName || c.name || c.phoneNumber || c.lid || "Unknown";
}

function formatLastSeen(ts: number | undefined): string {
  if (!ts) return "—";
  // Timestamps are unix seconds (int64); guard against ms just in case.
  const ms = ts < 1e12 ? ts * 1000 : ts;
  return new Date(ms).toLocaleString();
}

const ROLE_LABEL: Record<NonNullable<GroupRow["role"]>, string> = {
  member: "Member",
  admin: "Admin",
  superadmin: "Owner",
};

export default function ContactDetailRoute() {
  const { sessionId, lid } = useParams();
  const session = sessionId ?? "";
  const contactId = lid ?? "";
  const [search] = useSearchParams();

  const detail = useContact(session, contactId);

  if (detail.isLoading) {
    return (
      <Card>
        <CardHeader>
          <Skeleton className="h-6 w-40" />
        </CardHeader>
        <CardContent className="space-y-3">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-32 w-full" />
        </CardContent>
      </Card>
    );
  }

  if (detail.isError) {
    const notFound = isApiError(detail.error) && detail.error.code === "not_found";
    const msg = isApiError(detail.error)
      ? detail.error.message
      : "Failed to load contact";
    return (
      <Card>
        <CardContent className="space-y-3 pt-6">
          <p className="text-sm text-destructive">
            {notFound ? "Contact not found." : msg}
          </p>
          {!notFound && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => void detail.refetch()}
            >
              Retry
            </Button>
          )}
        </CardContent>
      </Card>
    );
  }

  const data = detail.data;
  const identity = data?.identity;
  const groups = data?.groups ?? [];
  // The JID we act on (picture/about/block) — prefer the contact lid.
  const jid = identity?.lid ?? contactId;

  return (
    <div className="space-y-4">
      <IdentityCard
        session={session}
        jid={jid}
        identity={identity}
        dm={Boolean(data?.dm)}
        search={search.toString()}
      />

      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            Group memberships
            <Badge variant="secondary" className="ml-2 align-middle">
              {groups.length}
            </Badge>
          </CardTitle>
        </CardHeader>
        <CardContent>
          {groups.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              Not found in any shared group.
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Group</TableHead>
                  <TableHead>Nickname</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>Last seen</TableHead>
                  <TableHead className="text-right">Filter</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {groups.map((g, i) => (
                  <GroupMembershipRow
                    key={g.jid ?? i}
                    session={session}
                    group={g}
                  />
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function IdentityCard({
  session,
  jid,
  identity,
  dm,
  search,
}: {
  session: string;
  jid: string;
  identity: Contact | undefined;
  dm: boolean;
  search: string;
}) {
  // Best-effort enrichment — both may be 501 in v1; we never block the page on
  // them and surface a friendly note instead of an error.
  const picture = useContactPicture(session, jid, Boolean(jid));
  const about = useContactAbout(session, jid, Boolean(jid));
  const block = useBlockContact(session);

  const pictureUnavailable =
    isApiError(picture.error) && picture.error.isNotImplemented;
  const aboutUnavailable =
    isApiError(about.error) && about.error.isNotImplemented;

  const runBlock = (shouldBlock: boolean): void => {
    block.mutate(
      { jid, block: shouldBlock },
      {
        onSuccess: () =>
          toast.success(shouldBlock ? "Contact blocked" : "Contact unblocked"),
        onError: (err) => {
          if (isApiError(err) && err.isNotImplemented) {
            toast.info("Block/unblock is not available in v1.");
            return;
          }
          toast.error(isApiError(err) ? err.message : "Action failed");
        },
      },
    );
  };

  const name = displayName(identity);

  return (
    <Card>
      <CardHeader className="flex-row items-start gap-4 space-y-0">
        <Avatar className="size-16">
          {picture.data?.url && !pictureUnavailable && (
            <AvatarImage src={picture.data.url} alt={name} />
          )}
          <AvatarFallback className="text-lg">
            {name.slice(0, 2).toUpperCase()}
          </AvatarFallback>
        </Avatar>
        <div className="min-w-0 flex-1 space-y-1">
          <CardTitle className="truncate text-lg">{name}</CardTitle>
          {identity?.phoneNumber && (
            <p className="text-sm text-muted-foreground">
              {identity.phoneNumber}
            </p>
          )}
          {identity?.lid && (
            <p className="truncate font-mono text-xs text-muted-foreground">
              {identity.lid}
            </p>
          )}
          <div className="flex flex-wrap gap-1.5 pt-1">
            {dm && <Badge variant="secondary">Has DM</Badge>}
            {identity?.source && (
              <Badge variant="outline" className="capitalize">
                Found in {identity.source}
              </Badge>
            )}
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <dl className="grid gap-3 text-sm sm:grid-cols-2">
          {identity?.pushName && identity.pushName !== name && (
            <Field label="Push name" value={identity.pushName} />
          )}
          {identity?.name && (
            <Field label="Saved name" value={identity.name} />
          )}
          <Field
            label="About"
            value={
              aboutUnavailable
                ? "Not available in v1"
                : about.isLoading
                  ? "Loading…"
                  : about.data?.about || "—"
            }
            muted={aboutUnavailable}
          />
        </dl>

        <div className="flex flex-wrap items-center gap-2 pt-1">
          {dm && (
            <Button asChild size="sm" variant="outline">
              <Link
                to={{
                  pathname: `/user/sessions/${encodeURIComponent(session)}/chats`,
                  search,
                }}
              >
                Open chats
              </Link>
            </Button>
          )}
          <Button
            size="sm"
            variant="outline"
            disabled={block.isPending}
            onClick={() => runBlock(true)}
          >
            Block
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={block.isPending}
            onClick={() => runBlock(false)}
          >
            Unblock
          </Button>
        </div>
        {pictureUnavailable && (
          <p className="text-xs text-muted-foreground">
            Profile picture is not available in v1.
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function GroupMembershipRow({
  session,
  group,
}: {
  session: string;
  group: GroupRow;
}) {
  const groupName = group.name || group.jid || "Unknown group";
  return (
    <TableRow>
      <TableCell className="font-medium">
        <span className="block max-w-[16rem] truncate">{groupName}</span>
        {group.jid && (
          <span className="block max-w-[16rem] truncate font-mono text-xs text-muted-foreground">
            {group.jid}
          </span>
        )}
      </TableCell>
      <TableCell>{group.nickname || "—"}</TableCell>
      <TableCell>
        {group.role ? (
          <Badge variant="outline">{ROLE_LABEL[group.role]}</Badge>
        ) : (
          "—"
        )}
      </TableCell>
      <TableCell className="text-muted-foreground">
        {formatLastSeen(group.lastSeen)}
      </TableCell>
      <TableCell className="text-right">
        {group.jid && (
          <Button asChild size="sm" variant="ghost" className="h-7 px-2">
            <Link
              to={{
                pathname: `/user/sessions/${encodeURIComponent(session)}/contacts`,
                search: `?source=group&group=${encodeURIComponent(group.jid)}`,
              }}
            >
              View members
            </Link>
          </Button>
        )}
      </TableCell>
    </TableRow>
  );
}

function Field({
  label,
  value,
  muted,
}: {
  label: string;
  value: string;
  muted?: boolean;
}) {
  return (
    <div>
      <dt className="text-xs font-medium text-muted-foreground">{label}</dt>
      <dd className={muted ? "text-muted-foreground" : ""}>{value}</dd>
    </div>
  );
}
