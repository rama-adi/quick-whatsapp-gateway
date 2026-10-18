// Chat composer: a soft rounded bar docked under the timeline. Text plus
// structured payloads (poll, location, contact) through the paperclip menu.
//
// Sends are optimistic (the bubble appears at once), so the box clears on
// submit; if the send fails, the parent's promise rejects and the typed text is
// put back (unless the user already started a new message).

import { useState, type ComponentProps, type FormEvent, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import {
  Contact,
  ListChecks,
  MapPin,
  Megaphone,
  Paperclip,
  SendHorizonal,
  X,
} from "lucide-react";
import type { Chat, Message, SessionStatus } from "~/lib/api/types";
import { cn } from "~/lib/utils";
import { Button } from "~/components/ui/button";
import { Textarea } from "~/components/ui/textarea";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Spinner } from "~/components/ui/spinner";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog";
import { ChatAvatar } from "./-viewer-ui";

export type ViewerChat = Chat & {
  participantCount?: number;
  isAnnounce?: boolean;
  isLocked?: boolean;
};

export type ComposerSend =
  | { type: "text"; text: string }
  | {
      type: "poll";
      name: string;
      options: string[];
      selectableCount: number;
      pollEndTime?: number;
      pollHideVotes?: boolean;
    }
  | { type: "location"; name?: string; latitude: number; longitude: number }
  | { type: "contact"; contact: { name: string; phone?: string; jid?: string } };

type RichKind = "poll" | "location" | "contact";

const OFFLINE_COPY: Partial<Record<SessionStatus, string>> = {
  stopped: "This session is stopped.",
  failed: "This session failed to connect.",
  starting: "This session is connecting…",
  scan_qr_code: "This session isn't linked to a phone yet.",
  logged_out: "This session was logged out.",
};

export function Composer({
  sessionId,
  sessionStatus,
  onStartSession,
  startingSession,
  sending,
  chat,
  typingText,
  replyTo,
  onCancelReply,
  onSend,
}: {
  sessionId: string;
  /** Undefined while the session is loading (composer stays usable). */
  sessionStatus?: SessionStatus;
  onStartSession: () => void;
  startingSession: boolean;
  sending: boolean;
  chat?: ViewerChat;
  typingText: string | null;
  replyTo: Message | null;
  onCancelReply: () => void;
  /** Resolves when the API accepted the send; rejects on failure. */
  onSend: (payload: ComposerSend) => Promise<unknown>;
}) {
  const [text, setText] = useState("");
  const [dialog, setDialog] = useState<RichKind | null>(null);

  const offline = sessionStatus !== undefined && sessionStatus !== "working";

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = text.trim();
    if (!trimmed || sending || offline) return;
    const typed = text;
    setText("");
    onSend({ type: "text", text: trimmed }).catch(() => {
      // Put the message back so a failed send never loses it.
      setText((cur) => (cur.trim() ? cur : typed));
    });
  };

  if (offline && sessionStatus) {
    const canStart = sessionStatus === "stopped" || sessionStatus === "failed";
    return (
      <div className="shrink-0 px-3 pt-1 pb-3 sm:px-4">
        <div className="flex flex-wrap items-center gap-3 rounded-2xl border border-dashed bg-background/70 px-4 py-3 text-sm">
          <p className="min-w-0 flex-1 text-muted-foreground">
            <span className="font-medium text-foreground">
              {OFFLINE_COPY[sessionStatus] ?? "This session isn't connected."}
            </span>{" "}
            {canStart
              ? "Start it to send and receive messages."
              : sessionStatus === "starting"
                ? "You can send once it's connected."
                : "Link the phone again from the overview page to send messages."}
          </p>
          {canStart ? (
            <Button size="sm" onClick={onStartSession} disabled={startingSession}>
              {startingSession ? <Spinner /> : null}
              Start session
            </Button>
          ) : sessionStatus === "starting" ? (
            <Spinner className="text-muted-foreground" />
          ) : (
            <Button size="sm" variant="outline" asChild>
              <Link to="/user/sessions/$sessionId" params={{ sessionId }}>
                Open overview
              </Link>
            </Button>
          )}
        </div>
      </div>
    );
  }

  const isAnnounce = chat?.type === "group" && chat.isAnnounce;
  const isChannel = chat?.type === "newsletter";

  return (
    <>
      <form
        onSubmit={submit}
        className="relative shrink-0 px-3 pt-1 pb-3 sm:px-4"
        aria-label="Send a message"
      >
        <TypingIndicator chat={chat} text={typingText} />
        {isAnnounce || isChannel ? (
          <p className="mb-1.5 flex items-center gap-1.5 px-1 text-xs text-muted-foreground">
            <Megaphone className="size-3.5 shrink-0" aria-hidden />
            {isChannel
              ? "Only channel admins can post. Sends fail if this number doesn't own the channel."
              : "Only group admins can send here. Sends fail if this number isn't an admin."}
          </p>
        ) : null}
        <div className="rounded-2xl border border-border/80 bg-background shadow-xs transition-[border-color,box-shadow] duration-200 ease-soft focus-within:border-ring focus-within:ring-3 focus-within:ring-ring/20 dark:bg-input/30">
          {replyTo ? (
            <div className="flex items-start gap-2 px-2 pt-2">
              <div className="min-w-0 flex-1 rounded-lg border-l-2 border-emerald-600/60 bg-muted/60 px-2.5 py-1.5 text-xs">
                <p className="font-medium">Replying to {replyAuthor(replyTo)}</p>
                <p className="truncate text-muted-foreground">{replyPreview(replyTo)}</p>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                className="mt-1 text-muted-foreground"
                onClick={onCancelReply}
                aria-label="Cancel reply"
              >
                <X aria-hidden />
              </Button>
            </div>
          ) : null}
          <div className="flex items-end gap-1 p-1.5">
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="rounded-xl text-muted-foreground hover:text-foreground"
                  aria-label="Send a poll, location or contact"
                  title="Send a poll, location or contact"
                >
                  <Paperclip className="size-4" aria-hidden />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" side="top" className="w-48">
                <DropdownMenuItem onClick={() => setDialog("poll")}>
                  <ListChecks /> Poll
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setDialog("location")}>
                  <MapPin /> Location
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setDialog("contact")}>
                  <Contact /> Contact card
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
            <Textarea
              value={text}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  (e.currentTarget.form as HTMLFormElement | null)?.requestSubmit();
                }
              }}
              placeholder={
                isChannel ? "Write an announcement" : "Message (supports *bold* and _italic_)"
              }
              aria-label="Message"
              aria-describedby="composer-hint"
              rows={1}
              className="max-h-40 min-h-8 flex-1 resize-none border-0 bg-transparent px-1.5 py-1.5 shadow-none focus-visible:border-0 focus-visible:ring-0 dark:bg-transparent"
            />
            <Button
              type="submit"
              size="icon"
              className="rounded-xl"
              disabled={sending || text.trim().length === 0}
              aria-label="Send"
            >
              {sending ? <Spinner /> : <SendHorizonal className="size-4" aria-hidden />}
            </Button>
          </div>
        </div>
        <p
          id="composer-hint"
          className="mt-1.5 hidden px-2 text-[11px] text-muted-foreground sm:block"
        >
          <Kbd>Enter</Kbd> to send · <Kbd>Shift</Kbd> + <Kbd>Enter</Kbd> for a new line
        </p>
      </form>
      <RichSendDialog
        kind={dialog}
        onOpenChange={(open) => {
          if (!open) setDialog(null);
        }}
        onSend={(payload) => {
          setDialog(null);
          void onSend(payload).catch(() => undefined);
        }}
      />
    </>
  );
}

function Kbd({ children }: { children: ReactNode }) {
  return (
    <kbd className="rounded border border-border/80 bg-muted/60 px-1 font-sans text-[10px] font-medium text-foreground/80">
      {children}
    </kbd>
  );
}

const RICH_COPY: Record<RichKind, { title: string; description: string }> = {
  poll: {
    title: "Send a poll",
    description: "People in this chat vote by tapping an option in WhatsApp.",
  },
  location: {
    title: "Send a location",
    description: "Shares a map pin. Copy coordinates from any map app.",
  },
  contact: {
    title: "Send a contact card",
    description: "Shares a contact that the recipient can save or message.",
  },
};

function RichSendDialog({
  kind,
  onOpenChange,
  onSend,
}: {
  kind: RichKind | null;
  onOpenChange: (open: boolean) => void;
  onSend: (payload: ComposerSend) => void;
}) {
  return (
    <Dialog open={kind !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        {kind ? (
          <>
            <DialogHeader>
              <DialogTitle>{RICH_COPY[kind].title}</DialogTitle>
              <DialogDescription>{RICH_COPY[kind].description}</DialogDescription>
            </DialogHeader>
            {kind === "poll" ? <PollForm onSend={onSend} /> : null}
            {kind === "location" ? <LocationForm onSend={onSend} /> : null}
            {kind === "contact" ? <ContactForm onSend={onSend} /> : null}
          </>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function PollForm({ onSend }: { onSend: (payload: ComposerSend) => void }) {
  const [question, setQuestion] = useState("");
  const [options, setOptions] = useState("");
  const [selectableCount, setSelectableCount] = useState("1");
  const [pollEndTime, setPollEndTime] = useState("");
  const [pollHideVotes, setPollHideVotes] = useState(false);
  const [touched, setTouched] = useState(false);

  const rows = options
    .split("\n")
    .map((x) => x.trim())
    .filter(Boolean);
  const unique = new Set(rows).size === rows.length;
  const questionError = !question.trim() ? "Add a question." : null;
  const optionsError =
    rows.length < 2
      ? "Add at least 2 options, one per line."
      : rows.length > 12
        ? "WhatsApp allows at most 12 options."
        : !unique
          ? "Each option must be different."
          : null;
  const valid = !questionError && !optionsError;

  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        setTouched(true);
        if (!valid) return;
        const selectable = Math.max(1, Math.min(Number(selectableCount) || 1, rows.length));
        const endTime = pollEndTime ? new Date(pollEndTime).getTime() : 0;
        onSend({
          type: "poll",
          name: question.trim(),
          options: rows,
          selectableCount: selectable,
          ...(Number.isFinite(endTime) && endTime > 0 ? { pollEndTime: endTime } : {}),
          ...(pollHideVotes ? { pollHideVotes: true } : {}),
        });
      }}
    >
      <Field
        id="poll-question"
        label="Question"
        value={question}
        onChange={setQuestion}
        placeholder="Where should we meet?"
        error={touched ? questionError : null}
        autoFocus
      />
      <div className="space-y-1.5">
        <Label htmlFor="poll-options">Options</Label>
        <Textarea
          id="poll-options"
          value={options}
          onChange={(e) => setOptions(e.target.value)}
          rows={4}
          placeholder={"One option per line\nCafe\nOffice"}
          aria-invalid={touched && optionsError ? true : undefined}
        />
        <FieldHint error={touched ? optionsError : null}>
          {rows.length > 0 ? `${rows.length} option${rows.length === 1 ? "" : "s"}` : "One option per line"}
        </FieldHint>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field
          id="poll-selectable"
          label="Answers per person"
          value={selectableCount}
          onChange={setSelectableCount}
          type="number"
          min={1}
          max={Math.max(1, rows.length)}
        />
        <Field
          id="poll-end"
          label="Closes at (optional)"
          value={pollEndTime}
          onChange={setPollEndTime}
          type="datetime-local"
        />
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={pollHideVotes}
          onChange={(e) => setPollHideVotes(e.currentTarget.checked)}
          className="size-4 accent-primary"
        />
        Hide who voted for what
      </label>
      <DialogFooter>
        <Button type="submit" disabled={touched && !valid}>
          Send poll
        </Button>
      </DialogFooter>
    </form>
  );
}

function LocationForm({ onSend }: { onSend: (payload: ComposerSend) => void }) {
  const [place, setPlace] = useState("");
  const [lat, setLat] = useState("");
  const [lng, setLng] = useState("");
  const [touched, setTouched] = useState(false);

  const latitude = Number(lat);
  const longitude = Number(lng);
  const latError =
    lat.trim() === "" || !Number.isFinite(latitude)
      ? "Enter a latitude."
      : latitude < -90 || latitude > 90
        ? "Latitude must be between -90 and 90."
        : null;
  const lngError =
    lng.trim() === "" || !Number.isFinite(longitude)
      ? "Enter a longitude."
      : longitude < -180 || longitude > 180
        ? "Longitude must be between -180 and 180."
        : null;
  const valid = !latError && !lngError;

  // Accept a pasted "lat, lng" pair in either field.
  const onPaste = (value: string): boolean => {
    const m = /^\s*(-?\d+(?:\.\d+)?)\s*[,\s]\s*(-?\d+(?:\.\d+)?)\s*$/.exec(value);
    if (!m) return false;
    setLat(m[1] ?? "");
    setLng(m[2] ?? "");
    return true;
  };

  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        setTouched(true);
        if (!valid) return;
        onSend({ type: "location", name: place.trim() || undefined, latitude, longitude });
      }}
    >
      <Field
        id="loc-place"
        label="Place name (optional)"
        value={place}
        onChange={setPlace}
        placeholder="Head office"
        autoFocus
      />
      <div className="grid grid-cols-2 gap-3">
        <Field
          id="loc-lat"
          label="Latitude"
          value={lat}
          onChange={(v) => onPaste(v) || setLat(v)}
          placeholder="-6.2088"
          inputMode="decimal"
          error={touched ? latError : null}
        />
        <Field
          id="loc-lng"
          label="Longitude"
          value={lng}
          onChange={(v) => onPaste(v) || setLng(v)}
          placeholder="106.8456"
          inputMode="decimal"
          error={touched ? lngError : null}
        />
      </div>
      <p className="text-xs text-muted-foreground">
        Tip: paste “-6.2088, 106.8456” into either field to fill both.
      </p>
      <DialogFooter>
        <Button type="submit" disabled={touched && !valid}>
          Send location
        </Button>
      </DialogFooter>
    </form>
  );
}

function ContactForm({ onSend }: { onSend: (payload: ComposerSend) => void }) {
  const [name, setName] = useState("");
  const [phone, setPhone] = useState("");
  const [touched, setTouched] = useState(false);
  const nameError = !name.trim() ? "Add a name." : null;
  const phoneError =
    phone.trim() && !/^\+?[\d\s().-]{5,}$/.test(phone.trim())
      ? "Use digits with the country code, e.g. +62 812 3456 7890."
      : null;
  const valid = !nameError && !phoneError;

  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        setTouched(true);
        if (!valid) return;
        onSend({
          type: "contact",
          contact: { name: name.trim(), phone: phone.trim() || undefined },
        });
      }}
    >
      <Field
        id="contact-name"
        label="Name"
        value={name}
        onChange={setName}
        placeholder="Alex Example"
        error={touched ? nameError : null}
        autoFocus
      />
      <Field
        id="contact-phone"
        label="Phone (optional)"
        value={phone}
        onChange={setPhone}
        placeholder="+62 812 3456 7890"
        inputMode="tel"
        error={touched ? phoneError : null}
      />
      <DialogFooter>
        <Button type="submit" disabled={touched && !valid}>
          Send contact
        </Button>
      </DialogFooter>
    </form>
  );
}

function Field({
  id,
  label,
  value,
  onChange,
  error,
  type = "text",
  ...rest
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  error?: string | null;
  type?: string;
} & Pick<
  ComponentProps<"input">,
  "placeholder" | "min" | "max" | "inputMode" | "autoFocus"
>) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type={type}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? `${id}-error` : undefined}
        {...rest}
      />
      {error ? (
        <p id={`${id}-error`} className="text-xs text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  );
}

function FieldHint({ error, children }: { error: string | null; children: ReactNode }) {
  return (
    <p className={cn("text-xs", error ? "text-destructive" : "text-muted-foreground")}>
      {error ?? children}
    </p>
  );
}

function TypingIndicator({ chat, text }: { chat?: ViewerChat; text: string | null }) {
  if (!text) return null;
  return (
    <div
      className="pointer-events-none absolute -top-6 left-4 flex h-6 items-center gap-1.5 rounded-full bg-background/90 px-2 text-[11px] text-muted-foreground shadow-xs ring-1 ring-border/60 sm:left-5"
      aria-live="polite"
    >
      <ChatAvatar
        sessionId={chat?.sessionId}
        name={chat?.name}
        jid={chat?.jid}
        type={chat?.type}
        className="size-4 text-[8px]"
      />
      <span className="animate-pulse">{text}</span>
    </div>
  );
}

function replyAuthor(message: Message): string {
  if (message.direction === "out") return "yourself";
  return message.senderName || message.senderLid || message.senderJid || "sender";
}

function replyPreview(message: Message): string {
  const body = message.body?.trim();
  if (body) return body.length > 140 ? `${body.slice(0, 140)}...` : body;
  return message.type ? `${message.type} message` : "Message";
}
