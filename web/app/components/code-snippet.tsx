// A rounded mono code block with a copy button and optional language tabs.
// No syntax highlighting on purpose (no deps). Build API request tabs with
// apiRequestSnippets() from ~/lib/snippets.
//
//   <CodeSnippet code={`GATEWAY_ID=${id}`} title=".env" />
//   <CodeSnippet tabs={apiRequestSnippets({ url, token })} />

import { useState, type ReactNode } from "react";
import { Tabs as TabsPrimitive } from "radix-ui";
import { CopyButton } from "~/components/copyable";
import type { SnippetTab } from "~/lib/snippets";
import { cn } from "~/lib/utils";

export type { SnippetTab } from "~/lib/snippets";

/** Code block with copy; pass `code` for one block or `tabs` for languages. */
export function CodeSnippet({
  code,
  tabs,
  defaultTab,
  title,
  copyLabel = "Copy code",
  className,
}: {
  /** Single snippet (ignored when `tabs` is given). */
  code?: string;
  /** Language tabs, e.g. curl / JavaScript / Python. */
  tabs?: readonly SnippetTab[];
  /** Initially selected tab id. Default: the first tab. */
  defaultTab?: string;
  /** Header caption for a single snippet (e.g. ".env", "Request"). */
  title?: ReactNode;
  /** Copy button tooltip / aria-label. Default "Copy code". */
  copyLabel?: string;
  className?: string;
}) {
  const [active, setActive] = useState(defaultTab ?? tabs?.[0]?.id ?? "");
  const current = tabs?.find((t) => t.id === active) ?? tabs?.[0];
  const text = tabs ? (current?.code ?? "") : (code ?? "");

  const body = (value: string) => (
    <pre className="max-h-96 overflow-auto p-3 font-mono text-xs leading-relaxed text-foreground">
      <code>{value}</code>
    </pre>
  );

  const shell = cn(
    "min-w-0 overflow-hidden rounded-xl border bg-muted/40 dark:bg-input/20",
    className,
  );
  const header = "flex h-9 items-center justify-between gap-2 border-b bg-muted/50 pr-1.5 pl-1";

  if (!tabs) {
    return (
      <div data-slot="code-snippet" className={shell}>
        <div className={header}>
          <span className="truncate pl-2 text-xs font-medium text-muted-foreground">{title}</span>
          <CopyButton value={text} label={copyLabel} iconOnly />
        </div>
        {body(text)}
      </div>
    );
  }

  return (
    <TabsPrimitive.Root
      data-slot="code-snippet"
      value={current?.id}
      onValueChange={setActive}
      className={shell}
    >
      <div className={header}>
        <TabsPrimitive.List aria-label="Language" className="flex min-w-0 items-center gap-0.5">
          {tabs.map((t) => (
            <TabsPrimitive.Trigger
              key={t.id}
              value={t.id}
              className="h-7 rounded-md px-2 text-xs font-medium whitespace-nowrap text-muted-foreground transition-colors duration-150 ease-soft outline-none hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/30 data-[state=active]:bg-background data-[state=active]:text-foreground data-[state=active]:shadow-xs dark:data-[state=active]:bg-input/50"
            >
              {t.label}
            </TabsPrimitive.Trigger>
          ))}
        </TabsPrimitive.List>
        <CopyButton value={text} label={copyLabel} iconOnly />
      </div>
      {tabs.map((t) => (
        <TabsPrimitive.Content key={t.id} value={t.id} className="outline-none">
          {body(t.code)}
        </TabsPrimitive.Content>
      ))}
    </TabsPrimitive.Root>
  );
}
