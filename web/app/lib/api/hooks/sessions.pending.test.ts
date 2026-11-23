// Session actions carry mutation keys, so a component sees an action that a
// different component (another useMutation instance) started.
import { describe, expect, it } from "vitest";
import { createElement } from "react";
import { renderToString } from "react-dom/server";
import { MutationObserver, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { sessionMutationKeys, usePendingSessionActions } from "./sessions";

function pendingIn(qc: QueryClient, ids: string[]): string {
  function Probe() {
    const pendingFor = usePendingSessionActions();
    return createElement("span", null, ids.map((id) => `${id}=${pendingFor(id) ?? "-"}`).join(","));
  }
  return renderToString(createElement(QueryClientProvider, { client: qc }, createElement(Probe)));
}

function startPending(qc: QueryClient, mutationKey: readonly unknown[], variables: unknown): void {
  const observer = new MutationObserver<unknown, Error, unknown>(qc, {
    mutationKey,
    mutationFn: () => new Promise(() => {}),
  });
  void observer.mutate(variables).catch(() => {});
}

describe("usePendingSessionActions", () => {
  it("sees lifecycle actions and deletes started elsewhere, per session", async () => {
    const qc = new QueryClient();
    startPending(qc, sessionMutationKeys.lifecycle, { sessionId: "a", action: "start" });
    startPending(qc, sessionMutationKeys.delete, { sessionId: "b" });
    await Promise.resolve();

    expect(pendingIn(qc, ["a", "b", "c"])).toContain("a=start,b=delete,c=-");
  });

  it("reports the newest pending action for a session", async () => {
    const qc = new QueryClient();
    startPending(qc, sessionMutationKeys.lifecycle, { sessionId: "a", action: "stop" });
    startPending(qc, sessionMutationKeys.lifecycle, { sessionId: "a", action: "restart" });
    await Promise.resolve();

    expect(pendingIn(qc, ["a"])).toContain("a=restart");
  });
});
