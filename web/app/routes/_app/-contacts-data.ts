// Contacts surface — server-side hybrid READS (§6.2). Colocated server functions
// that read the GATEWAY-OWNED identity/group tables directly via Drizzle for
// SSR/loader hydration, mapped into the OpenAPI DTO shapes the gateway REST API
// returns (Contact / ContactDetail / Page<Contact>) so the client hooks
// (useContacts / useContact) hydrate from the seeded cache.
//
// READ-ONLY (single-writer = gateway, §6.2). Gated to the caller's active org.
//
// "Found users" is a PROJECTION over the central whatsapp_identities table:
// there is no contacts table. A person is found in a DM when the session has a
// `chats` row (type='dm') whose peer is their LID/phone, and in a group via
// whatsapp_group_members. This mirrors the gateway's ContactRepo query.

import { createServerFn } from "@tanstack/react-start";
import { authMiddleware } from "~/lib/auth/middleware";
import type { Contact, ContactDetail, ContactFilter } from "~/lib/api/types";
import type { Page } from "~/lib/api/envelope";

const PAGE_LIMIT = 50;

async function assertSessionInActiveOrg(
  sessionId: string,
  activeOrgId: string | null,
): Promise<boolean> {
  if (!activeOrgId) return false;
  const { db } = await import("~/lib/db");
  const { waSessions } = await import("~/lib/db/wa");
  const { and, eq } = await import("drizzle-orm");
  const rows = await db
    .select({ id: waSessions.id })
    .from(waSessions)
    .where(
      and(
        eq(waSessions.id, sessionId),
        eq(waSessions.organizationId, activeOrgId),
      ),
    )
    .limit(1);
  return rows.length > 0;
}

/** SSR seed for the found-users list (page 0). Cursor by identity surrogate id. */
export const fetchContactsPage = createServerFn({ method: "GET" })
  .middleware([authMiddleware])
  .validator(
    (input: { sessionId: string; filter: ContactFilter; cursor?: string }) =>
      input,
  )
  .handler(async ({ data, context }): Promise<Page<Contact>> => {
    const { sessionId, filter, cursor } = data;
    const ok = await assertSessionInActiveOrg(
      sessionId,
      context.activeOrg?.id ?? null,
    );
    if (!ok) return { data: [], nextCursor: null };

    const { db } = await import("~/lib/db");
    const { whatsappIdentities } = await import("~/lib/db/wa");
    const { and, eq, gt, like, or, asc, sql } = await import("drizzle-orm");

    // Per-session "found" signals, correlated to each identity row.
    const dmExists = sql<boolean>`EXISTS (SELECT 1 FROM chats ch
      WHERE ch.session_id = ${sessionId} AND ch.type = 'dm'
        AND (ch.chat_jid = ${whatsappIdentities.lid}
          OR (${whatsappIdentities.phoneJid} IS NOT NULL AND ch.chat_jid = ${whatsappIdentities.phoneJid})))`;
    const groupExists = sql<boolean>`EXISTS (SELECT 1 FROM whatsapp_group_members gm
      WHERE gm.session_id = ${sessionId} AND gm.lid = ${whatsappIdentities.lid})`;

    const conds = [];
    const cursorId = cursor ? Number(cursor) : undefined;
    if (cursorId !== undefined && Number.isFinite(cursorId)) {
      conds.push(gt(whatsappIdentities.id, cursorId));
    }
    if (filter.group) {
      conds.push(
        sql`EXISTS (SELECT 1 FROM whatsapp_group_members gm
          WHERE gm.session_id = ${sessionId} AND gm.lid = ${whatsappIdentities.lid}
            AND gm.group_jid = ${filter.group})`,
      );
    } else if (filter.source === "dm") {
      conds.push(dmExists);
    } else if (filter.source === "group") {
      conds.push(groupExists);
    } else {
      const anywhere = or(dmExists, groupExists);
      if (anywhere) conds.push(anywhere);
    }
    if (filter.q) {
      const term = `%${filter.q}%`;
      const m = or(
        like(whatsappIdentities.name, term),
        like(whatsappIdentities.phoneNumber, term),
        like(whatsappIdentities.lid, term),
      );
      if (m) conds.push(m);
    }

    const rows = await db
      .select({
        identityId: whatsappIdentities.id,
        lid: whatsappIdentities.lid,
        phoneNumber: whatsappIdentities.phoneNumber,
        name: whatsappIdentities.name,
        businessName: whatsappIdentities.businessName,
        inDm: dmExists,
        inGroup: groupExists,
      })
      .from(whatsappIdentities)
      .where(and(...conds))
      .orderBy(asc(whatsappIdentities.id))
      .limit(PAGE_LIMIT + 1);

    const hasMore = rows.length > PAGE_LIMIT;
    const pageRows = hasMore ? rows.slice(0, PAGE_LIMIT) : rows;

    return {
      data: pageRows.map(rowToContact),
      nextCursor: hasMore
        ? String(pageRows[pageRows.length - 1]?.identityId)
        : null,
    };
  });

/** SSR seed for a contact drill-in: identity + dm flag + group memberships. */
export const fetchContactDetail = createServerFn({ method: "GET" })
  .middleware([authMiddleware])
  .validator((input: { sessionId: string; lid: string }) => input)
  .handler(async ({ data, context }): Promise<ContactDetail | null> => {
    const { sessionId, lid } = data;
    const ok = await assertSessionInActiveOrg(
      sessionId,
      context.activeOrg?.id ?? null,
    );
    if (!ok) return null;

    const { db } = await import("~/lib/db");
    const { whatsappIdentities, whatsappGroupMembers, whatsappGroups } =
      await import("~/lib/db/wa");
    const { and, eq, sql } = await import("drizzle-orm");

    const idRows = await db
      .select({
        lid: whatsappIdentities.lid,
        phoneNumber: whatsappIdentities.phoneNumber,
        name: whatsappIdentities.name,
        businessName: whatsappIdentities.businessName,
        phoneJid: whatsappIdentities.phoneJid,
      })
      .from(whatsappIdentities)
      .where(eq(whatsappIdentities.lid, lid))
      .limit(1);

    const idRow = idRows[0];
    if (!idRow) return null;

    const dmRows = await db
      .select({
        found: sql<boolean>`EXISTS (SELECT 1 FROM chats
          WHERE session_id = ${sessionId} AND type = 'dm'
            AND (chat_jid = ${lid}
              OR (${idRow.phoneJid} IS NOT NULL AND chat_jid = ${idRow.phoneJid})))`,
      })
      .from(sql`DUAL`);
    const dm = Boolean(dmRows[0]?.found);

    const groupRows = await db
      .select({
        jid: whatsappGroupMembers.groupJid,
        name: whatsappGroups.subject,
        tag: whatsappGroupMembers.tag,
        role: whatsappGroupMembers.role,
        lastSeen: whatsappGroupMembers.lastSeenAt,
      })
      .from(whatsappGroupMembers)
      .leftJoin(
        whatsappGroups,
        eq(whatsappGroups.groupJid, whatsappGroupMembers.groupJid),
      )
      .where(
        and(
          eq(whatsappGroupMembers.sessionId, sessionId),
          eq(whatsappGroupMembers.lid, lid),
        ),
      );

    return {
      identity: rowToContact({
        lid: idRow.lid,
        phoneNumber: idRow.phoneNumber,
        name: idRow.name,
        businessName: idRow.businessName,
        inDm: dm,
        inGroup: groupRows.length > 0,
      }),
      dm,
      groups: groupRows.map((g) => ({
        jid: g.jid,
        name: g.name ?? undefined,
        tag: g.tag ?? undefined,
        role: g.role,
        lastSeen: g.lastSeen ?? undefined,
      })),
    };
  });

// ===== row -> DTO mapper (mirrors the gateway's REST Contact shape) =====

type IdentityRow = {
  lid: string;
  phoneNumber: string | null;
  name: string | null;
  businessName: string | null;
  inDm: boolean;
  inGroup: boolean;
};

function rowToContact(r: IdentityRow): Contact {
  return {
    lid: r.lid,
    phoneNumber: r.phoneNumber ?? undefined,
    name: r.name ?? undefined,
    businessName: r.businessName ?? undefined,
    // A direct relationship is the stronger signal; else it's a group find.
    source: r.inDm ? "dm" : "group",
  };
}
