// Admin session status comes from the cached admin rows, which cacheBridge
// patches only from session.status events. A failed message must not mark the
// session failed (message.status payloads also carry status: "failed").
import { describe, expect, it } from "vitest";
import { QueryClient, type InfiniteData } from "@tanstack/react-query";
import { applyEvent } from "~/lib/events/cacheBridge";
import { qk } from "~/lib/query";
import type { Page } from "~/lib/api/envelope";
import type { EventEnvelope, WASession } from "~/lib/api/types";

const row: WASession = {
  id: "sess_1",
  organizationId: "org_1",
  gatewayId: "gw_1",
  status: "working",
  isAdminSession: true,
  desiredState: "run",
  statusChangedAt: 1,
  autoRead: false,
  presenceTyping: false,
  ratePerMin: 10,
  ratePerHour: 100,
  historySyncDays: 30,
  createdAt: 1,
  updatedAt: 1,
};

function evt(event: string, payload: Record<string, unknown>): EventEnvelope {
  return {
    schema: "v1",
    id: `evt_${event}`,
    event,
    session: row.id,
    organization: row.organizationId,
    timestamp: 1000,
    payload,
  } as EventEnvelope;
}

describe("admin session status", () => {
  it("is unaffected by a failed message", () => {
    const qc = new QueryClient();
    qc.setQueryData<InfiniteData<Page<WASession>, string | undefined>>(qk.adminSessions(), {
      pageParams: [undefined],
      pages: [{ data: [row], nextCursor: null }],
    });

    applyEvent(qc, evt("message.status", { messageIds: ["m1"], status: "failed", chatJid: "x@s.whatsapp.net" }));

    const data = qc.getQueryData<InfiniteData<Page<WASession>>>(qk.adminSessions());
    expect(data?.pages[0]?.data[0]?.status).toBe("working");
  });
});
