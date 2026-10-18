// Page chrome: lets a route project content (back button, title, tabs, actions)
// into the app top bar, project a section-tab row directly under it, and opt
// the main content region into "fill" mode.
//
// The top bar (SiteHeader) is global chrome rendered ABOVE the <Outlet> by the
// AppShell, so a page can't render into it directly. SiteHeader exposes a slot
// via context; <PageHeader> portals its children into that slot for as long as
// the page is mounted. <PageSubnav> does the same for an optional second row
// under the header (a layout's section tabs, e.g. one session's Overview /
// Chats / Contacts), so the tabs stay fixed above the scroller. <main> normally has padding and scrolls; a page that
// manages its own internal scrolling (e.g. chat) opts into "fill" mode — no
// padding, height-clamped to the viewport, page owns the scroll — via the
// `fill` prop on <PageHeader> or the standalone useFillMain() hook (for a leaf
// whose header is owned by a parent layout).

import {
  createContext,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { Link } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { Button } from "~/components/ui/button";
import { cn } from "~/lib/utils";

interface PageChromeContextValue {
  headerSlot: HTMLElement | null;
  setHeaderSlot: (el: HTMLElement | null) => void;
  subnavSlot: HTMLElement | null;
  setSubnavSlot: (el: HTMLElement | null) => void;
  fill: boolean;
  setFill: (fill: boolean) => void;
}

const PageChromeContext = createContext<PageChromeContextValue | null>(null);

function usePageChromeContext(): PageChromeContextValue {
  const ctx = useContext(PageChromeContext);
  if (!ctx) {
    throw new Error("Page chrome must render under <PageChromeProvider>.");
  }
  return ctx;
}

export function PageChromeProvider({ children }: { children: ReactNode }) {
  const [headerSlot, setHeaderSlot] = useState<HTMLElement | null>(null);
  const [subnavSlot, setSubnavSlot] = useState<HTMLElement | null>(null);
  const [fill, setFill] = useState(false);
  return (
    <PageChromeContext.Provider
      value={{
        headerSlot,
        setHeaderSlot,
        subnavSlot,
        setSubnavSlot,
        fill,
        setFill,
      }}
    >
      {children}
    </PageChromeContext.Provider>
  );
}

/** Whether the active page asked for a full-height, no-padding main. */
export function usePageFill(): boolean {
  return usePageChromeContext().fill;
}

/**
 * Clamp <main> to the viewport (no padding, page owns its scrolling) while the
 * calling component is mounted. Use this when a parent layout already owns the
 * top-bar header (so you can't pass `fill` on <PageHeader>), e.g. the chat
 * surface nested under the session-detail tabs.
 */
export function useFillMain(enabled = true): void {
  const { setFill } = usePageChromeContext();
  useEffect(() => {
    if (!enabled) return;
    setFill(true);
    return () => setFill(false);
  }, [enabled, setFill]);
}

/** The top-bar slot that <PageHeader> portals into. Rendered by SiteHeader. */
export function PageHeaderSlot({ className }: { className?: string }) {
  const { setHeaderSlot } = usePageChromeContext();
  return <div ref={setHeaderSlot} className={className} />;
}

/** The second-row slot under the top bar that <PageSubnav> portals into. */
export function PageSubnavSlot({ className }: { className?: string }) {
  const { setSubnavSlot } = usePageChromeContext();
  return <div ref={setSubnavSlot} className={className} />;
}

/**
 * Project a section-tab row under the top bar for as long as this component is
 * mounted. Like <PageHeader>, render it once, from the layout that owns the
 * sections, so the row stays put while its child routes swap below it.
 */
export function PageSubnav({ children }: { children: ReactNode }) {
  const { subnavSlot } = usePageChromeContext();
  return subnavSlot ? createPortal(children, subnavSlot) : null;
}

/**
 * Project content into the app top bar for as long as this page is mounted, and
 * (optionally) switch <main> to fill mode. Render ONCE per surface — in the
 * route that owns it; nested routes that also render <PageHeader> would both
 * portal into the same slot. Example:
 *
 *   <PageHeader>
 *     <HeaderBack to="/user/sessions" label="All sessions" />
 *     <HeaderTitle>Acme WhatsApp</HeaderTitle>
 *   </PageHeader>
 */
export function PageHeader({
  fill = false,
  children,
}: {
  fill?: boolean;
  children: ReactNode;
}) {
  const { headerSlot, setFill } = usePageChromeContext();
  useEffect(() => {
    if (!fill) return;
    setFill(true);
    return () => setFill(false);
  }, [fill, setFill]);
  return headerSlot ? createPortal(children, headerSlot) : null;
}

/**
 * Consistent top-bar title. This is the header's context label (e.g. the open
 * session's name), not the page heading: page bodies open with <PageTitle>
 * from page-title.tsx.
 */
export function HeaderTitle({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "min-w-0 truncate text-sm font-medium text-foreground",
        className,
      )}
    >
      {children}
    </span>
  );
}

/** Consistent top-bar back button (icon + label, label hidden on narrow). */
export function HeaderBack({
  to,
  params,
  label = "Back",
}: {
  to: string;
  params?: Record<string, string>;
  label?: string;
}) {
  return (
    <Button
      asChild
      variant="ghost"
      size="sm"
      className="-ml-1 shrink-0 gap-1.5 text-muted-foreground hover:text-foreground"
    >
      {/* `to` is a runtime-dynamic route string; cast past the typed-route union. */}
      <Link to={to as never} params={params as never} aria-label={label}>
        <ArrowLeftIcon className="size-4" aria-hidden />
        <span className="hidden sm:inline">{label}</span>
      </Link>
    </Button>
  );
}
