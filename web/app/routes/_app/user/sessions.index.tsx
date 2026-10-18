// User: sessions — list + create. §12 user surface, scoped to the active org.
//
// This is the INDEX of /user/sessions. The sibling sessions.tsx is a thin layout
// that only renders an <Outlet/>, so this list shows at /user/sessions while the
// detail surfaces (sessions.$sessionId and its children) render in that same
// Outlet — without the list bleeding through underneath them.
//
// Creating a session navigates straight to its page, where pairing happens.
// Live status is kept fresh by the event-stream cacheBridge (session.status).

import { useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import {
  ChevronDownIcon,
  ChevronRightIcon,
  PlusIcon,
  QrCodeIcon,
  RefreshCwIcon,
  SmartphoneIcon,
} from "lucide-react";
import { useSessions, useCreateSession } from "~/lib/api/hooks/sessions";
import type { CreateSessionRequest, WASession } from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Skeleton } from "~/components/ui/skeleton";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "~/components/ui/dialog";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "~/components/ui/empty";
import { Page, PageTitle } from "~/components/shell/page-title";
import { SessionStatusBadge } from "~/components/status-badge";
import { Time } from "~/components/time";
import { DOCS } from "~/lib/docs-links";
import { titleHead } from "~/lib/head";
import { cn } from "~/lib/utils";
import { toast } from "sonner";
import {
  SessionActions,
  formatPhone,
  sessionName,
  statusHint,
  useSessionActions,
} from "./-components/user-ui";

export const Route = createFileRoute("/_app/user/sessions/")({
  head: () => titleHead("Sessions"),
  component: SessionsList,
});

function SessionsList() {
  const sessions = useSessions();
  const [createOpen, setCreateOpen] = useState(false);
  const rows = sessions.data?.pages.flatMap((p) => p.data ?? []) ?? [];

  return (
    <Page>
      <PageTitle
        title="Sessions"
        description="Each session is one linked WhatsApp number."
        docsHref={DOCS.connectNumber}
        actions={<CreateSessionDialog open={createOpen} onOpenChange={setCreateOpen} />}
      />

      {sessions.isLoading ? (
        <div className="grid gap-3">
          <Skeleton className="h-[4.5rem] w-full rounded-xl" />
          <Skeleton className="h-[4.5rem] w-full rounded-xl" />
        </div>
      ) : sessions.isError ? (
        <Empty>
          <EmptyHeader>
            <EmptyTitle>Couldn't load sessions</EmptyTitle>
            <EmptyDescription>
              {isApiError(sessions.error)
                ? sessions.error.message
                : "Something went wrong while loading your sessions."}
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button variant="outline" onClick={() => void sessions.refetch()}>
              <RefreshCwIcon aria-hidden data-icon="inline-start" />
              Retry
            </Button>
          </EmptyContent>
        </Empty>
      ) : rows.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <SmartphoneIcon aria-hidden />
            </EmptyMedia>
            <EmptyTitle>Connect your first number</EmptyTitle>
            <EmptyDescription>
              A session links one WhatsApp number to the gateway. Create one,
              then scan a QR code with your phone, just like WhatsApp Web.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent className="flex-row justify-center">
            <Button onClick={() => setCreateOpen(true)}>
              <PlusIcon aria-hidden data-icon="inline-start" />
              New session
            </Button>
            <Button asChild variant="ghost">
              <a href={DOCS.connectNumber} target="_blank" rel="noreferrer">
                Read the guide
              </a>
            </Button>
          </EmptyContent>
        </Empty>
      ) : (
        <SessionList rows={rows} />
      )}

      {sessions.hasNextPage && (
        <div className="flex justify-center">
          <Button
            variant="outline"
            disabled={sessions.isFetchingNextPage}
            onClick={() => void sessions.fetchNextPage()}
          >
            {sessions.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}
    </Page>
  );
}

function SessionList({ rows }: { rows: WASession[] }) {
  const actions = useSessionActions();
  return (
    <ul className="grid gap-2.5">
      {rows.map((s) => (
        <SessionRow key={s.id} session={s} actions={actions} />
      ))}
    </ul>
  );
}

function SessionRow({
  session: s,
  actions,
}: {
  session: WASession;
  actions: ReturnType<typeof useSessionActions>;
}) {
  const paired = Boolean(s.waJid);
  const phone = formatPhone(s.phoneNumber);
  const hint = statusHint(s);

  return (
    <li className="group relative flex flex-col gap-3 rounded-xl border bg-card p-4 shadow-[0_1px_2px_rgb(0_0_0/0.04)] transition-colors duration-150 ease-soft hover:bg-muted/30 focus-within:ring-3 focus-within:ring-ring/30 sm:flex-row sm:items-center sm:gap-4">
      <span
        className={cn(
          "hidden size-10 shrink-0 items-center justify-center rounded-xl ring-1 ring-border sm:flex",
          s.status === "working"
            ? "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400"
            : "bg-muted text-muted-foreground",
        )}
      >
        <SmartphoneIcon className="size-5" aria-hidden />
      </span>

      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
          {/* The whole row is the link target (stretched ::after); the
              action buttons sit above it with `relative z-10`. */}
          <Link
            to="/user/sessions/$sessionId"
            params={{ sessionId: s.id }}
            className="min-w-0 truncate font-medium outline-none after:absolute after:inset-0 after:rounded-xl"
          >
            {sessionName(s)}
          </Link>
          <SessionStatusBadge status={s.status} className="relative z-10" />
        </div>
        <p className="mt-0.5 truncate text-sm text-muted-foreground">
          {phone ? <span className="tabular-nums">{phone}</span> : "Not linked yet"}
          <span aria-hidden> · </span>
          created <Time value={s.createdAt} />
          {hint && s.status !== "working" && (
            <>
              <span aria-hidden> · </span>
              {hint}
            </>
          )}
        </p>
      </div>

      <div className="relative z-10 flex shrink-0 items-center gap-1.5">
        {paired ? (
          <SessionActions session={s} actions={actions} />
        ) : (
          <>
            <Button asChild size="sm">
              <Link to="/user/sessions/$sessionId" params={{ sessionId: s.id }}>
                <QrCodeIcon aria-hidden data-icon="inline-start" />
                Link phone
              </Link>
            </Button>
            <SessionActions session={s} actions={actions} showPrimary={false} />
          </>
        )}
        <ChevronRightIcon
          className="hidden size-4 text-muted-foreground transition-transform duration-150 ease-soft group-hover:translate-x-0.5 sm:block"
          aria-hidden
        />
      </div>
    </li>
  );
}

function CreateSessionDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [label, setLabel] = useState("");
  const [advanced, setAdvanced] = useState(false);
  const [start, setStart] = useState(true);
  const [autoRead, setAutoRead] = useState(false);
  const [presenceTyping, setPresenceTyping] = useState(false);
  const create = useCreateSession();
  const navigate = useNavigate();

  const reset = (): void => {
    setLabel("");
    setAdvanced(false);
    setStart(true);
    setAutoRead(false);
    setPresenceTyping(false);
  };

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    const body: CreateSessionRequest = {
      label: label.trim() || undefined,
      start,
      autoRead,
      presenceTyping,
    };
    create.mutate(body, {
      onError: (err) =>
        toast.error(isApiError(err) ? err.message : "Couldn't create the session"),
      onSuccess: (created) => {
        reset();
        onOpenChange(false);
        // Straight to the new session's page: pairing is the next step.
        void navigate({
          to: "/user/sessions/$sessionId",
          params: { sessionId: created.id },
        });
      },
    });
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        onOpenChange(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger asChild>
        <Button>
          <PlusIcon aria-hidden data-icon="inline-start" />
          New session
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="grid gap-5">
          <DialogHeader>
            <DialogTitle>New session</DialogTitle>
            <DialogDescription>
              Give it a name you'll recognize. Next, you'll link your phone by
              scanning a QR code.
            </DialogDescription>
          </DialogHeader>

          <div className="grid gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="session-label">Name</Label>
              <Input
                id="session-label"
                value={label}
                onChange={(e) => setLabel(e.target.value)}
                placeholder="e.g. Support line"
                autoFocus
              />
            </div>

            <div className="grid gap-3">
              <button
                type="button"
                className="inline-flex w-fit items-center gap-1 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground"
                aria-expanded={advanced}
                aria-controls="session-advanced"
                onClick={() => setAdvanced((v) => !v)}
              >
                <ChevronDownIcon
                  className={cn(
                    "size-4 transition-transform duration-200 ease-soft",
                    !advanced && "-rotate-90",
                  )}
                  aria-hidden
                />
                Advanced options
              </button>
              {advanced && (
                <div
                  id="session-advanced"
                  className="grid gap-3.5 rounded-lg border bg-muted/30 p-3.5"
                >
                  <CheckboxRow
                    id="session-start"
                    label="Start right away"
                    description="Shows a QR code as soon as the session exists. Turn off if you'll link with a phone-number code instead."
                    checked={start}
                    onChange={setStart}
                  />
                  <CheckboxRow
                    id="session-autoread"
                    label="Auto-read messages"
                    description="Mark incoming messages as read automatically. Can't be changed later."
                    checked={autoRead}
                    onChange={setAutoRead}
                  />
                  <CheckboxRow
                    id="session-typing"
                    label="Show typing indicator"
                    description="Show “typing…” to the recipient while a message is sent. Can't be changed later."
                    checked={presenceTyping}
                    onChange={setPresenceTyping}
                  />
                </div>
              )}
            </div>
          </div>

          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? "Creating…" : "Create and link phone"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function CheckboxRow({
  id,
  label,
  description,
  checked,
  onChange,
}: {
  id: string;
  label: string;
  description: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-start gap-3">
      <input
        id={id}
        type="checkbox"
        className="mt-0.5 size-4 accent-primary"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
      <div className="grid gap-0.5">
        <Label htmlFor={id} className="font-normal">
          {label}
        </Label>
        <p className="text-xs text-muted-foreground">{description}</p>
      </div>
    </div>
  );
}
