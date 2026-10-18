// Viewer: the "no chat selected" pane (desktop only; on mobile the chats route
// shows just the list here). Points at the next step instead of a blank pane.

import { createFileRoute, Link } from "@tanstack/react-router";
import { MessageCirclePlus, MessagesSquare } from "lucide-react";
import { useSession } from "~/lib/api/hooks/sessions";
import { SessionStatusBadge } from "~/components/status-badge";
import { Button } from "~/components/ui/button";
import { NewChatDialog } from "./-new-chat";

export const Route = createFileRoute(
  "/_app/user/sessions/$sessionId/chats/",
)({
  component: ChatEmptyState,
});

function ChatEmptyState() {
  const { sessionId } = Route.useParams();
  const session = useSession(sessionId);
  const status = session.data?.status;
  const working = status === "working";
  const label = session.data?.label || "this session";
  const phone = session.data?.phoneNumber;

  return (
    <section
      aria-label="No chat selected"
      className="flex h-full min-h-0 flex-1 flex-col items-center justify-center bg-muted/70 p-8 dark:bg-background"
    >
      <div className="flex max-w-sm flex-col items-center gap-4 text-center">
        <span className="flex size-12 items-center justify-center rounded-2xl bg-background text-muted-foreground shadow-xs ring-1 ring-border">
          <MessagesSquare className="size-6" aria-hidden />
        </span>
        <div className="space-y-1.5">
          <h2 className="text-base font-semibold tracking-tight">Select a chat</h2>
          <p className="text-sm text-pretty text-muted-foreground">
            Pick a conversation on the left to read and reply as{" "}
            <span className="font-medium text-foreground">{label}</span>
            {phone ? ` (+${phone.replace(/^\+/, "")})` : ""}, or start a new
            one with a contact or phone number.
          </p>
        </div>
        {status ? <SessionStatusBadge status={status} /> : null}
        <div className="flex flex-wrap items-center justify-center gap-2">
          <NewChatDialog
            sessionId={sessionId}
            canCheckNumbers={working}
            trigger={
              <Button size="sm" className="gap-1.5">
                <MessageCirclePlus aria-hidden />
                New chat
              </Button>
            }
          />
          {!working && status ? (
            <Button size="sm" variant="outline" asChild>
              <Link to="/user/sessions/$sessionId" params={{ sessionId }}>
                Open overview
              </Link>
            </Button>
          ) : null}
        </div>
      </div>
    </section>
  );
}
