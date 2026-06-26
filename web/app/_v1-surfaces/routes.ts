import {
  type RouteConfig,
  route,
  layout,
  index,
  prefix,
} from "@react-router/dev/routes";

// Route tree (FROZEN — authored once by the foundation agent). Each surface owns
// a disjoint dir under app/routes/; this file references the leaf modules but no
// surface agent edits it. A missing route is a request to the foundation agent.
export default [
  // AUTH surface — public, no shell.
  layout("routes/auth/layout.tsx", [
    route("login", "routes/auth/login.tsx"),
    route("register", "routes/auth/register.tsx"),
    route("2fa", "routes/auth/totp.tsx"),
  ]),

  // Authenticated app shell — clientLoader = requireSession(); mounts the stream.
  layout("components/shell/AppShell.tsx", [
    index("routes/home.tsx"), // role-routed landing

    // ADMIN surface (super_admin).
    ...prefix("admin", [
      index("routes/admin/index.tsx"),
      route("tenants", "routes/admin/tenants.tsx"),
      route("sessions", "routes/admin/sessions.tsx"),
      route("monitor", "routes/admin/monitor.tsx"),
      route("pairing", "routes/admin/pairing.tsx"),
    ]),

    // USER surface (user role + userPanelEnabled).
    ...prefix("user", [
      ...prefix("sessions", [
        index("routes/user/sessions.list.tsx"),
        route(":sessionId", "routes/user/session.detail.tsx", [
          index("routes/user/session.overview.tsx"),

          // VIEWER surface (read-only, nested under a session).
          route("chats", "routes/viewer/chats.tsx", [
            route(":chatId", "routes/viewer/timeline.tsx"),
          ]),

          // CONTACTS surface (found users, nested under a session).
          route("contacts", "routes/contacts/list.tsx", [
            route(":lid", "routes/contacts/detail.tsx"),
          ]),
        ]),
      ]),
      route("keys", "routes/user/keys.tsx"),
      route("webhooks", "routes/user/webhooks.tsx"),
    ]),
  ]),
] satisfies RouteConfig;
