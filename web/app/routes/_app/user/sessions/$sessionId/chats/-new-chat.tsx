// "New chat" dialog for the chats surface. One search box does two jobs:
//   - filters contacts this session has already seen (GET /contacts), and
//   - when the text looks like a phone number, offers "Message +<number>",
//     which verifies the number with GET /contacts/check before opening it.
// Rendered by the chat list header and by the "no chat selected" pane.

import { useMemo, useState, type ReactNode } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useQueryClient, type InfiniteData } from "@tanstack/react-query";
import { ArrowRight, Phone, Search } from "lucide-react";
import { useContacts } from "~/lib/api/hooks/contacts";
import { apiUrl, fetchJSON } from "~/lib/api/client";
import { isApiError, type Page } from "~/lib/api/envelope";
import type { Chat, Contact } from "~/lib/api/types";
import { qk } from "~/lib/query";
import { Button } from "~/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "~/components/ui/dialog";
import { Input } from "~/components/ui/input";
import { Skeleton } from "~/components/ui/skeleton";
import { Spinner } from "~/components/ui/spinner";
import { ChatAvatar } from "./-viewer-ui";

type OnWhatsApp = { isOnWhatsApp: boolean; jid?: string; query: string };

/** Digits of a phone-like query ("+62 812-3456" → "628123456"), or null. */
export function phoneDigits(value: string): string | null {
  const trimmed = value.trim();
  if (!/^\+?[\d\s().-]+$/.test(trimmed)) return null;
  const digits = trimmed.replace(/\D/g, "");
  return digits.length >= 7 && digits.length <= 15 ? digits : null;
}

function seedChat(
  qc: ReturnType<typeof useQueryClient>,
  sessionId: string,
  jid: string,
  name: string,
) {
  qc.setQueryData<Chat>(qk.chat(sessionId, jid), (cur) =>
    cur ?? {
      id: 0,
      sessionId,
      jid,
      type: "dm",
      name,
      unreadCount: 0,
      archived: false,
      pinned: false,
    },
  );
}

export function NewChatDialog({
  sessionId,
  canCheckNumbers,
  trigger,
}: {
  sessionId: string;
  /** The live number lookup needs a connected session. */
  canCheckNumbers: boolean;
  trigger: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [checking, setChecking] = useState(false);
  const [checkError, setCheckError] = useState<string | null>(null);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const digits = phoneDigits(q);
  // Contacts store bare digits, so search "+62 812…" as "62812…".
  const contacts = useContacts(sessionId, { q: (digits ?? q.trim()) || undefined });
  const rows = useMemo(
    () =>
      (contacts.data as InfiniteData<Page<Contact>> | undefined)?.pages.flatMap(
        (p) => p.data,
      ) ?? [],
    [contacts.data],
  );

  const close = () => {
    setOpen(false);
    setQ("");
    setCheckError(null);
  };

  const messageNumber = async () => {
    if (!digits || checking) return;
    setChecking(true);
    setCheckError(null);
    try {
      const res = await fetchJSON<OnWhatsApp>(
        apiUrl(
          `/sessions/${encodeURIComponent(sessionId)}/contacts/check?phone=${encodeURIComponent(digits)}`,
        ),
      );
      if (!res.isOnWhatsApp || !res.jid) {
        setCheckError(`+${digits} isn't on WhatsApp. Check the country code and try again.`);
        return;
      }
      seedChat(qc, sessionId, res.jid, `+${digits}`);
      close();
      void navigate({
        to: "/user/sessions/$sessionId/chats/$chatId",
        params: { sessionId, chatId: res.jid },
      });
    } catch (e) {
      setCheckError(isApiError(e) ? e.message : "Couldn't check that number. Try again.");
    } finally {
      setChecking(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => (next ? setOpen(true) : close())}
    >
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent className="gap-0 p-0 sm:max-w-md">
        <DialogHeader className="px-5 pt-5 pb-3">
          <DialogTitle>New chat</DialogTitle>
          <DialogDescription>
            Pick a contact, or type a phone number with its country code.
          </DialogDescription>
        </DialogHeader>
        <div className="px-5 pb-3">
          <div className="relative">
            <Search
              className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
              aria-hidden
            />
            <Input
              value={q}
              onChange={(e) => {
                setQ(e.target.value);
                setCheckError(null);
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter" && digits && canCheckNumbers) {
                  e.preventDefault();
                  void messageNumber();
                }
              }}
              className="pl-8"
              placeholder="Name or phone, e.g. +62 812 3456 7890"
              aria-label="Search contacts or enter a phone number"
              autoFocus
            />
          </div>
        </div>

        {digits ? (
          <div className="px-3 pb-2">
            <button
              type="button"
              disabled={!canCheckNumbers || checking}
              onClick={() => void messageNumber()}
              className="flex w-full items-center gap-3 rounded-lg bg-muted/60 px-2.5 py-2 text-left transition-colors duration-150 ease-soft hover:bg-muted disabled:cursor-not-allowed disabled:opacity-60"
            >
              <span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-emerald-500/12 text-emerald-700 dark:text-emerald-400">
                {checking ? <Spinner /> : <Phone className="size-4" aria-hidden />}
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm font-medium">
                  Message +{digits}
                </span>
                <span className="block truncate text-xs text-muted-foreground">
                  {canCheckNumbers
                    ? checking
                      ? "Checking WhatsApp…"
                      : "We'll check the number is on WhatsApp first"
                    : "Connect this session to message new numbers"}
                </span>
              </span>
              <ArrowRight className="size-4 shrink-0 text-muted-foreground" aria-hidden />
            </button>
            {checkError ? (
              <p role="alert" className="px-1 pt-2 text-xs text-destructive">
                {checkError}
              </p>
            ) : null}
          </div>
        ) : null}

        <div className="max-h-80 min-h-40 overflow-y-auto border-t border-border/60 px-3 py-2 [scrollbar-width:thin]">
          <p className="px-2 pt-1 pb-1.5 text-xs font-medium text-muted-foreground">
            Contacts
          </p>
          <ContactRows
            sessionId={sessionId}
            rows={rows}
            hasQuery={Boolean(q.trim())}
            isLoading={contacts.isLoading}
            isError={contacts.isError}
            onRetry={() => void contacts.refetch()}
            onPick={(contact) => {
              seedChat(qc, sessionId, contact.lid, contactDisplayName(contact));
              close();
            }}
          />
          {contacts.hasNextPage ? (
            <div className="p-2">
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
          ) : null}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function ContactRows({
  sessionId,
  rows,
  hasQuery,
  isLoading,
  isError,
  onRetry,
  onPick,
}: {
  sessionId: string;
  rows: Contact[];
  hasQuery: boolean;
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
  onPick: (contact: Contact) => void;
}) {
  if (isLoading) {
    return (
      <ul className="space-y-1">
        {Array.from({ length: 4 }).map((_, i) => (
          <li key={i} className="flex items-center gap-3 px-2 py-2">
            <Skeleton className="size-9 shrink-0 rounded-full" />
            <Skeleton className="h-4 flex-1" />
          </li>
        ))}
      </ul>
    );
  }
  if (isError) {
    return (
      <div className="space-y-3 px-2 py-3 text-sm">
        <p className="text-destructive">Couldn't load contacts.</p>
        <Button size="sm" variant="outline" onClick={onRetry}>
          Retry
        </Button>
      </div>
    );
  }
  if (rows.length === 0) {
    return (
      <p className="px-2 py-3 text-sm text-pretty text-muted-foreground">
        {hasQuery
          ? "No contacts match. To message someone new, type their phone number with the country code."
          : "No contacts yet. People appear here once they message this number, or you can type a phone number above."}
      </p>
    );
  }
  return (
    <ul className="space-y-0.5">
      {rows.map((contact) => (
        <li key={contact.lid}>
          <Link
            to="/user/sessions/$sessionId/chats/$chatId"
            params={{ sessionId, chatId: contact.lid }}
            preload={false}
            onClick={() => onPick(contact)}
            className="flex items-center gap-3 rounded-lg px-2 py-2 text-left transition-colors duration-150 ease-soft hover:bg-muted"
          >
            <ChatAvatar
              sessionId={sessionId}
              name={contactDisplayName(contact)}
              jid={contact.lid}
              type="dm"
            />
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-medium">
                {contactDisplayName(contact)}
              </div>
              <div className="truncate text-xs text-muted-foreground">
                {contact.phoneNumber ? `+${contact.phoneNumber.replace(/^\+/, "")}` : contact.lid}
              </div>
            </div>
          </Link>
        </li>
      ))}
    </ul>
  );
}

export function contactDisplayName(contact: Contact): string {
  return (
    contact.name ||
    contact.businessName ||
    contact.phoneNumber ||
    contact.lid ||
    "Unknown"
  );
}
