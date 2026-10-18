// Decorative right half of the "Get started" card: a WhatsApp chat bubble on a
// dotted grid, with the JSON events the gateway emits for it drifting upward
// behind (message in, event out). Pure CSS/SVG, aria-hidden, still under
// prefers-reduced-motion, and hidden below `lg`. The quick links sit on top.

import { ArrowUpRightIcon } from "lucide-react";
import { DOCS } from "~/lib/docs-links";

const EVENT_LINES = [
  `{ "event": "message.received",`,
  `  "session": "sess_01M57MYX5702…",`,
  `  "payload": { "from": "6281234567890",`,
  `    "type": "text", "text": "Is my order on the way?" } }`,
  ``,
  `{ "event": "message.status",`,
  `  "payload": { "id": "3EB0C4F1A9…", "status": "delivered" } }`,
  ``,
  `{ "event": "session.status",`,
  `  "payload": { "status": "working" } }`,
  ``,
  `{ "event": "message.received",`,
  `  "payload": { "from": "6289876543210",`,
  `    "type": "image", "caption": "Receipt" } }`,
  ``,
  `{ "event": "presence",`,
  `  "payload": { "chat": "6281234567890", "state": "typing" } }`,
  ``,
  `{ "event": "message.status",`,
  `  "payload": { "id": "3EB0D2E7B1…", "status": "read" } }`,
  ``,
];

const KEYFRAMES = `
@keyframes home-art-drift { from { transform: translateY(0); } to { transform: translateY(-50%); } }
@media (prefers-reduced-motion: reduce) { [data-home-art-drift] { animation: none !important; } }
`;

export function ArtPanel() {
  return (
    <div className="relative hidden min-h-72 overflow-hidden border-l bg-muted/30 lg:block">
      <style>{KEYFRAMES}</style>

      {/* Dotted grid, fading toward the steps column. */}
      <div
        aria-hidden
        className="absolute inset-0 [mask-image:linear-gradient(to_right,transparent,black_45%)] [background-image:radial-gradient(var(--color-border)_1px,transparent_1px)] [background-size:14px_14px]"
      />

      {/* Soft emerald glow behind the event stream. */}
      <div
        aria-hidden
        className="absolute -top-16 right-[-10%] size-80 rounded-full bg-emerald-400/20 blur-3xl dark:bg-emerald-500/15"
      />

      {/* Event stream (two copies, so the -50% drift loops seamlessly). */}
      <div
        aria-hidden
        className="absolute inset-y-0 right-0 left-[46%] overflow-hidden [mask-image:linear-gradient(to_bottom,transparent,black_18%,black_62%,transparent_88%)]"
      >
        <div className="size-full pl-4 [mask-image:linear-gradient(to_right,transparent,black_22%,black_80%,transparent)]">
          <div
            data-home-art-drift
            className="font-mono text-[11px] leading-5 whitespace-pre text-muted-foreground/55"
            style={{ animation: "home-art-drift 48s linear infinite" }}
          >
            {[0, 1].map((copy) => (
              <div key={copy}>
                {EVENT_LINES.map((line, i) => (
                  <div key={i} className="h-5">
                    {line.includes(`"event"`) ? (
                      <>
                        {line.slice(0, line.indexOf(`"event"`))}
                        <span className="text-emerald-700/60 dark:text-emerald-400/60">
                          {line.slice(line.indexOf(`"event"`))}
                        </span>
                      </>
                    ) : (
                      line
                    )}
                  </div>
                ))}
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* The message that started it. */}
      <div aria-hidden className="absolute top-8 left-8 grid w-52 gap-2">
        <div className="relative w-fit max-w-full rounded-2xl rounded-tl-md bg-background px-3 py-2 text-xs shadow-xs ring-1 ring-border">
          Is my order on the way?
          <span className="ml-2 align-bottom text-[10px] text-muted-foreground">09:41</span>
        </div>
        <div className="relative ml-auto w-fit max-w-full rounded-2xl rounded-tr-md bg-emerald-50 px-3 py-2 text-xs text-emerald-950 shadow-xs ring-1 ring-emerald-600/15 dark:bg-emerald-950/60 dark:text-emerald-50 dark:ring-emerald-400/20">
          Yes, it ships today.
          <span className="ml-2 align-bottom text-[10px] text-emerald-800/60 dark:text-emerald-200/60">
            09:42
          </span>
        </div>
      </div>

      {/* Quick links. */}
      <div className="absolute inset-x-4 bottom-4 grid grid-cols-2 gap-3">
        <QuickLink
          href={DOCS.sending}
          title="API quickstart"
          description="Send your first message from code"
        />
        <QuickLink
          href={DOCS.apiReference}
          title="API reference"
          description="Every endpoint, field, and event"
        />
      </div>
    </div>
  );
}

export function QuickLink({ href, title, description }: { href: string; title: string; description: string }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className="group/ql flex min-w-0 flex-col gap-0.5 rounded-lg bg-background/85 px-3.5 py-3 shadow-xs ring-1 ring-border backdrop-blur-sm transition-all duration-200 ease-soft outline-none hover:bg-background hover:shadow-md focus-visible:ring-3 focus-visible:ring-ring/30"
    >
      <span className="flex items-center gap-1 text-sm font-medium">
        {title}
        <ArrowUpRightIcon
          aria-hidden
          className="size-3.5 text-muted-foreground transition-transform duration-200 ease-soft group-hover/ql:translate-x-0.5 group-hover/ql:-translate-y-0.5"
        />
      </span>
      <span className="truncate text-xs text-muted-foreground">{description}</span>
    </a>
  );
}
