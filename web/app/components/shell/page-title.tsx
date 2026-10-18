// Page-body layout primitives: the width/spacing wrapper and the title block
// every non-fill page opens with. (The top-bar slot is a different thing: see
// <PageHeader> in page-chrome.tsx, which portals into the header.)

import type { ReactNode } from "react";
import { cn } from "~/lib/utils";

const PAGE_WIDTH = {
  /** Lists, tables, dashboards: fill the shell's max-w-screen-2xl. */
  full: "",
  /** Detail pages with a sidebar-ish layout or wide cards. */
  "4xl": "max-w-4xl",
  /** Forms and settings: one comfortable reading column. */
  "3xl": "max-w-3xl",
} as const;

/**
 * Page body wrapper: vertical rhythm (`gap-6` between sections) and an
 * optional max width, left-aligned with the title.
 *
 * ```tsx
 * <Page>                       // lists / tables
 * <Page width="3xl">           // forms / settings
 * ```
 */
export function Page({
  width = "full",
  className,
  children,
}: {
  width?: keyof typeof PAGE_WIDTH;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      data-slot="page"
      className={cn("flex w-full flex-col gap-6", PAGE_WIDTH[width], className)}
    >
      {children}
    </div>
  );
}

/**
 * The title block at the top of a page body: an `<h1>`, a one-line
 * description, and right-aligned actions. Use it once per page, as the first
 * child of <Page>, instead of hand-rolling `<h1 className="text-xl …">` rows.
 *
 * ```tsx
 * <Page>
 *   <PageTitle
 *     title="API Keys"
 *     description="Let your servers call the gateway on behalf of this organization."
 *     actions={<Button size="sm">New key</Button>}
 *   />
 *   <TableCard>…</TableCard>
 * </Page>
 * ```
 *
 * - `title` / `description` accept nodes (e.g. a status badge after the name,
 *   or a mono ID as the description).
 * - `eyebrow` sits above the title: a back link or breadcrumb on detail pages.
 * - `actions` collapse under the title on narrow screens.
 */
export function PageTitle({
  title,
  description,
  actions,
  eyebrow,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  eyebrow?: ReactNode;
  className?: string;
}) {
  return (
    <div
      data-slot="page-title"
      className={cn(
        "flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between sm:gap-6",
        className,
      )}
    >
      <div className="flex min-w-0 flex-col gap-1">
        {eyebrow ? (
          <div className="text-sm text-muted-foreground">{eyebrow}</div>
        ) : null}
        <h1 className="flex min-w-0 items-center gap-2 text-xl font-semibold tracking-tight">
          {title}
        </h1>
        {description ? (
          <div className="text-sm text-pretty text-muted-foreground">
            {description}
          </div>
        ) : null}
      </div>
      {actions ? (
        <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>
      ) : null}
    </div>
  );
}
