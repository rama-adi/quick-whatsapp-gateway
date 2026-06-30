import { createFileRoute } from "@tanstack/react-router";
import { MessageCircle, MessageSquareText, MousePointer2 } from "lucide-react";
import type { ReactNode } from "react";
import {
  MessageScroller,
  MessageScrollerProvider,
  MessageScrollerViewport,
  MessageScrollerContent,
} from "~/components/ui/message-scroller";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "~/components/ui/empty";
import { Badge } from "~/components/ui/badge";

export const Route = createFileRoute(
  "/_app/user/sessions/$sessionId/chats/",
)({
  component: ChatEmptyState,
});

function ChatEmptyState() {
  return (
    <section
      aria-label="No chat selected"
      className="flex h-full min-h-[60svh] flex-col overflow-hidden rounded-lg border bg-card md:min-h-0"
    >
      <MessageScrollerProvider defaultScrollPosition="end">
        <MessageScroller className="min-h-0 flex-1">
          <MessageScrollerViewport
            className="outline-none"
            role="region"
            aria-label="Chat showcase empty state"
          >
            <MessageScrollerContent className="min-h-full justify-center gap-4 p-6">
              <Empty className="mx-auto max-w-lg border-0">
                <EmptyHeader>
                  <EmptyMedia variant="icon">
                    <MessageCircle />
                  </EmptyMedia>
                  <EmptyTitle>Select a chat</EmptyTitle>
                  <EmptyDescription>
                    Pick a conversation to inspect messages, send rich WhatsApp
                    payloads, and review contact or community context.
                  </EmptyDescription>
                </EmptyHeader>
              </Empty>
              <div className="mx-auto grid w-full max-w-xl gap-2 sm:grid-cols-3">
                <ShowcaseItem icon={<MessageSquareText />} label="Markdown" />
                <ShowcaseItem icon={<MousePointer2 />} label="Polls + location" />
                <ShowcaseItem icon={<MessageCircle />} label="Live timeline" />
              </div>
            </MessageScrollerContent>
          </MessageScrollerViewport>
        </MessageScroller>
      </MessageScrollerProvider>
    </section>
  );
}

function ShowcaseItem({ icon, label }: { icon: ReactNode; label: string }) {
  return (
    <div className="flex items-center justify-center gap-2 rounded-md border bg-background px-3 py-2 text-sm">
      <span className="[&_svg]:size-4 [&_svg]:text-muted-foreground">{icon}</span>
      <Badge variant="secondary" className="font-normal">
        {label}
      </Badge>
    </div>
  );
}
