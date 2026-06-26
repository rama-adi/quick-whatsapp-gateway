// Session context: the AppShell loader resolves the AppSession and publishes it
// here so (a) nav/components can read role/userPanelEnabled and (b) the event
// stream knows when it's safe to connect.
// FROZEN — owned by the foundation agent.

import { createContext, useContext } from "react";
import type { AppSession } from "./session";

const SessionContext = createContext<AppSession | null>(null);

export function SessionProvider({
  session,
  children,
}: {
  session: AppSession | null;
  children: React.ReactNode;
}) {
  return <SessionContext.Provider value={session}>{children}</SessionContext.Provider>;
}

/** Read the current session; null outside the authenticated shell. */
export function useSessionContext(): AppSession | null {
  return useContext(SessionContext);
}

/** Read the session, asserting it exists (use inside the authenticated shell). */
export function useAppSession(): AppSession {
  const s = useContext(SessionContext);
  if (!s) {
    throw new Error("useAppSession must be used within an authenticated shell");
  }
  return s;
}
