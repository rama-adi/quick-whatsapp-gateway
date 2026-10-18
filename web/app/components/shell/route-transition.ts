// Route content fade-in for the app shell.
//
// The page region eases in (fade + 4px rise) whenever the "surface" changes.
// It animates the existing element with the Web Animations API instead of
// re-keying it, so navigation never remounts the page tree (no state reset, no
// refetch). The key is the rendered leaf pathname, collapsed for master/detail
// surfaces whose child swaps in place: switching chats or contacts must not
// re-animate the whole page, only entering the surface does.

import { useLayoutEffect, useRef } from "react";
import { useRouterState } from "@tanstack/react-router";

// Surfaces that keep their frame while a nested child (the open chat / the
// open contact) changes. Everything under the match collapses to one key.
const STABLE_SURFACES: readonly RegExp[] = [
  /^\/user\/sessions\/[^/]+\/(?:chats|contacts)(?=\/|$)/,
];

/** The identity a navigation must change for the page to animate in. */
export function routeTransitionKey(pathname: string): string {
  const path = pathname.length > 1 ? pathname.replace(/\/+$/, "") : pathname;
  for (const surface of STABLE_SURFACES) {
    const match = surface.exec(path);
    if (match) return match[0];
  }
  return path;
}

const ENTER_KEYFRAMES: Keyframe[] = [
  { opacity: 0, transform: "translateY(4px)" },
  { opacity: 1, transform: "none" },
];
const ENTER_TIMING: KeyframeAnimationOptions = {
  duration: 300,
  easing: "cubic-bezier(0.22, 1, 0.36, 1)", // --ease-soft
};

/**
 * Attach to the element that wraps the route <Outlet/>. Skips the first paint
 * (SSR content is already visible) and honors prefers-reduced-motion.
 */
export function useRouteTransition<T extends HTMLElement>() {
  // Leaf of the *rendered* matches, so the animation starts on the commit that
  // swaps the content in (not when a pending navigation begins).
  const pathname = useRouterState({
    select: (s) => s.matches[s.matches.length - 1]?.pathname ?? s.location.pathname,
  });
  const key = routeTransitionKey(pathname);
  const ref = useRef<T>(null);
  const lastKey = useRef(key);

  useLayoutEffect(() => {
    if (lastKey.current === key) return;
    lastKey.current = key;
    const el = ref.current;
    if (!el || typeof el.animate !== "function") return;
    if (window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) return;
    const animation = el.animate(ENTER_KEYFRAMES, ENTER_TIMING);
    return () => animation.cancel();
  }, [key]);

  return ref;
}
