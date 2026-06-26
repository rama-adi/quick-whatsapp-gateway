# Dashboard SPA (M7)

Realtime dashboard for the WhatsApp gateway. React Router (framework mode) in
**SPA mode** + shadcn/ui + TanStack Query, built to static assets and embedded
in the Go binary.

## Commands

```bash
pnpm install
pnpm dev        # vite dev server on :5173, proxies /api + /auth to :8080
pnpm build      # → build/client (copied to internal/http/static/dist at image build)
pnpm typecheck  # react-router typegen && tsc (strict)
pnpm test       # vitest (parseNdjson + applyEvent)
pnpm gen:api    # regenerate app/lib/api/schema.d.ts from ../docs/openapi.yaml
```

The dev server proxies same-origin so the Authula **cookie session** and the
NDJSON **event stream** work without CORS. Run the Go backend on `:8080`.

## Architecture

- `app/lib/**` and `app/components/{ui,shell}/**` are the **frozen shared
  boundary** — owned by the foundation, imported by surface agents, never edited
  by them.
- Surface agents own one disjoint dir under `app/routes/` each:
  `auth/`, `admin/`, `user/`, `viewer/`, `contacts/`.
- The route tree is authored once in `app/routes.ts` (frozen).

### Shared modules (import paths)

| Module | Path | What |
|---|---|---|
| API client | `~/lib/api/client` | `fetchJSON<T>`, `apiUrl` (cookie auth) |
| Envelope | `~/lib/api/envelope` | `ApiError`, `isApiError`, `Page<T>` |
| Domain types | `~/lib/api/types` | `WASession`, `Message`, `Chat`, … |
| Query | `~/lib/query` | `queryClient`, `qk` (key factory) |
| Resource hooks | `~/lib/api/hooks/*` | sessions, chats, messages, contacts, groups, keys, webhooks, admin |
| Auth client | `~/lib/auth/client` | `signIn/signUp/signOut/totpVerify`, `authFetch` |
| Session/guards | `~/lib/auth/session` | `loadSession`, `requireSession`, `requireRole` |
| Session context | `~/lib/auth/context` | `useAppSession`, `useSessionContext` |
| Authula admin | `~/lib/auth/admin` | `useTenants/useBanTenant/useImpersonate` |
| Event stream | `~/lib/events/useEventStream` | `useEventStream()` status |
| Event firehose | `~/lib/events/eventBus` | `useEventBus(filter)` |
| Shell | `~/components/shell/*` | `AppShell`, nav, `ConnectionPill` |
| UI | `~/components/ui/*` | shadcn primitives |

Add more shadcn components with `pnpm dlx shadcn@latest add <name>`.
