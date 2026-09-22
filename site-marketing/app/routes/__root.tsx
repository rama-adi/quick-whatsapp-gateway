import { createRootRoute, HeadContent, Scripts } from '@tanstack/react-router';
import type { ReactNode } from 'react';
import appCss from '~/app.css?url';

export const Route = createRootRoute({
  head: () => ({
    meta: [
      { charSet: 'utf-8' },
      { name: 'viewport', content: 'width=device-width, initial-scale=1' },
      { title: 'WhatsApp Gateway — Messaging on your infrastructure' },
      { name: 'description', content: 'Connect WhatsApp sessions to your applications with a self-hosted gateway, REST API, and real-time events. Explore the complete guides and architecture.' },
    ],
    links: [{ rel: 'stylesheet', href: appCss }],
  }),
  shellComponent: ({ children }: { children: ReactNode }) => (
    <html lang="en" suppressHydrationWarning>
      <head><HeadContent /></head>
      <body>{children}<Scripts /></body>
    </html>
  ),
  notFoundComponent: () => <main className="landing"><h1>Page not found</h1><a href="/docs">Browse the documentation</a></main>,
});
