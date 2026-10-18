// Browser-tab titles for TanStack Router `head()`: "<Page> · WA Gateway".
//
//   export const Route = createFileRoute("/_app/user/keys")({
//     head: () => titleHead("API keys"),
//     component: Keys,
//   });

/** Product name used as the title suffix. */
export const APP_NAME = "WA Gateway";

/** Join title parts (most specific first) with " · " and append the app name; empty parts are skipped. */
export function pageTitle(...parts: Array<string | null | undefined | false>): string {
  return [...parts.filter((p): p is string => Boolean(p)), APP_NAME].join(" · ");
}

/** A `head()` result that sets only the document title, e.g. `head: () => titleHead("API keys")`. */
export function titleHead(...parts: Array<string | null | undefined | false>): {
  meta: Array<{ title: string }>;
} {
  return { meta: [{ title: pageTitle(...parts) }] };
}
