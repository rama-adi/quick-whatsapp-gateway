// The ordering rule for cached session rows: events, fetch results and
// mutation responses keep whichever side is newer.
import { beforeEach, describe, expect, it } from "vitest";
import { QueryClient, QueryObserver, type InfiniteData } from "@tanstack/react-query";
import { applyEvent } from "../events/cacheBridge";
import { qk } from "../query";
import type { Page } from "../api/envelope";
import type { EventEnvelope, WASession } from "../api/types";
import {
  mergeSession,
  mergeSessionPages,
  sessionPagesStructuralSharing,
  sessionStructuralSharing,
  writeSessionResponse,
} from "./merge";

const SESSION = "sess_1";

function row(over: Partial<WASession> = {}): WASession {
  return {
    id: SESSION,
    organizationId: "org_1",
    gatewayId: "gw_1",
    status: "stopped",
    isAdminSession: false,
    autoRead: false,
    presenceTyping: false,
    ratePerMin: 10,
    ratePerHour: 100,
    historySyncDays: 30,
    createdAt: 1,
    updatedAt: 100,
    ...over,
  };
}

function statusEvent(status: string, timestamp: number): EventEnvelope {
  return {
    schema: "v1",
    id: `evt_${status}_${timestamp}`,
    event: "session.status",
    session: SESSION,
    organization: "org_1",
    timestamp,
    payload: { status },
  } as EventEnvelope;
}

function pages(rows: WASession[]): InfiniteData<Page<WASession>, string | undefined> {
  return { pageParams: [undefined], pages: [{ data: rows, nextCursor: null }] };
}

describe("mergeSession", () => {
  it("takes the incoming row when it is newer or equally new", () => {
    expect(mergeSession(row({ updatedAt: 100 }), row({ status: "working", updatedAt: 200 })).status).toBe("working");
    expect(mergeSession(row({ updatedAt: 100 }), row({ status: "working", updatedAt: 100 })).status).toBe("working");
  });

  it("keeps the cached row when the incoming row is older and disagrees", () => {
    const cur = row({ status: "working", updatedAt: 200 });
    expect(mergeSession(cur, row({ status: "scan_qr_code", updatedAt: 150 }))).toBe(cur);
  });

  it("takes an older row's fields when the status agrees (clock skew)", () => {
    const cur = row({ status: "working", updatedAt: 200 });
    const merged = mergeSession(cur, row({ status: "working", waJid: "62@s.whatsapp.net", updatedAt: 150 }));
    expect(merged.waJid).toBe("62@s.whatsapp.net");
    expect(merged.updatedAt).toBe(200);
  });

  it("merges list rows by id", () => {
    const cur = pages([row({ status: "working", updatedAt: 200 }), row({ id: "other", updatedAt: 1 })]);
    const next = mergeSessionPages(cur, pages([row({ status: "scan_qr_code", updatedAt: 150 }), row({ id: "other", status: "working", updatedAt: 2 })]));
    expect(next.pages[0]?.data.map((r) => r.status)).toEqual(["working", "working"]);
  });
});

describe("session.status events", () => {
  let qc: QueryClient;
  beforeEach(() => {
    qc = new QueryClient();
  });

  it("apply when newer than the cached row and stamp updatedAt", () => {
    qc.setQueryData(qk.session(SESSION), row({ status: "starting", updatedAt: 100 }));
    applyEvent(qc, statusEvent("scan_qr_code", 300));
    expect(qc.getQueryData<WASession>(qk.session(SESSION))).toMatchObject({ status: "scan_qr_code", updatedAt: 300 });
  });

  it("are dropped when older than the cached row, and the row is refetched", () => {
    qc.setQueryData(qk.session(SESSION), row({ status: "working", updatedAt: 500 }));
    qc.setQueryData(qk.sessionQR(SESSION), { code: "2@live" });
    applyEvent(qc, statusEvent("starting", 300));
    expect(qc.getQueryData<WASession>(qk.session(SESSION))?.status).toBe("working");
    // An old event must not end the current attempt's artifacts either.
    expect(qc.getQueryData(qk.sessionQR(SESSION))).toEqual({ code: "2@live" });
    expect(qc.getQueryState(qk.session(SESSION))?.isInvalidated).toBe(true);
  });
});

describe("query writes go through the rule", () => {
  let qc: QueryClient;
  beforeEach(() => {
    qc = new QueryClient();
  });

  it("a fetch that read before a newer event cannot roll the detail row back", async () => {
    let resolve!: (r: WASession) => void;
    const observer = new QueryObserver<WASession>(qc, {
      queryKey: qk.session(SESSION),
      queryFn: () => new Promise<WASession>((r) => { resolve = r; }),
      structuralSharing: sessionStructuralSharing,
    });
    const unsubscribe = observer.subscribe(() => {});
    qc.setQueryData(qk.session(SESSION), row({ status: "starting", updatedAt: 100 }));

    // The event lands while the GET is in flight; the GET read the old row.
    applyEvent(qc, statusEvent("scan_qr_code", 300));
    resolve(row({ status: "starting", updatedAt: 100 }));
    await new Promise((r) => setTimeout(r, 0));

    expect(qc.getQueryData<WASession>(qk.session(SESSION))?.status).toBe("scan_qr_code");
    unsubscribe();
  });

  it("a stale list fetch cannot roll a list row back", () => {
    qc.setQueryData(qk.sessions(), pages([row({ status: "working", updatedAt: 500 })]));
    const merged = sessionPagesStructuralSharing(
      qc.getQueryData(qk.sessions()),
      pages([row({ status: "scan_qr_code", updatedAt: 400 })]),
    ) as InfiniteData<Page<WASession>>;
    expect(merged.pages[0]?.data[0]?.status).toBe("working");
  });

  it("keeps the same reference when nothing changed", () => {
    const cur = row();
    expect(sessionStructuralSharing(cur, { ...cur })).toBe(cur);
  });
});

describe("lifecycle responses", () => {
  it("replace an older cached row (no stale 'Start the session' after :start)", () => {
    const qc = new QueryClient();
    qc.setQueryData(qk.session(SESSION), row({ status: "stopped", updatedAt: 100 }));
    writeSessionResponse(qc, row({ status: "starting", updatedAt: 200 }));
    expect(qc.getQueryData<WASession>(qk.session(SESSION))?.status).toBe("starting");
  });

  it("do not replace a newer event's status", () => {
    const qc = new QueryClient();
    qc.setQueryData(qk.session(SESSION), row({ status: "stopped", updatedAt: 100 }));
    qc.setQueryData(qk.adminSessions(), pages([row({ status: "stopped", updatedAt: 100 })]));
    applyEvent(qc, statusEvent("scan_qr_code", 300));
    writeSessionResponse(qc, row({ status: "stopped", updatedAt: 100 }));
    expect(qc.getQueryData<WASession>(qk.session(SESSION))?.status).toBe("scan_qr_code");
    const admin = qc.getQueryData<InfiniteData<Page<WASession>>>(qk.adminSessions());
    expect(admin?.pages[0]?.data[0]?.status).toBe("scan_qr_code");
  });
});
