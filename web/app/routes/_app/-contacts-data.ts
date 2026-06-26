// Contacts surface — server-side hybrid READS (§6.2). Colocated server functions
// that read the GATEWAY-OWNED identity/contacts/groups tables directly via
// Drizzle for SSR/loader hydration, mapped into the OpenAPI DTO shapes the
// gateway REST API returns (Contact / ContactDetail / Page<Contact>) so the
// client hooks (useContacts / useContact) hydrate from the seeded cache.
//
// READ-ONLY (single-writer = gateway, §6.2). Gated to the caller's active org:
// the session must belong to the active organization before exposing its data.
//
// Filters mirror the gateway's contact list query (?q=&source=&group=). The
// list joins whatsapp_contacts (per-session, the "found in DM" flag) with the
// global whatsapp_identities (name/phone), and optionally with
// whatsapp_group_members (source=group / a specific group).

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
    const { whatsappContacts, whatsappIdentities, whatsappGroupMembers } =
      await import("~/lib/db/wa");
    const { and, eq, gt, like, or, asc } = await import("drizzle-orm");

    const cursorId = cursor ? Number(cursor) : undefined;
    const conds = [eq(whatsappContacts.sessionId, sessionId)];

    if (cursorId !== undefined && Number.isFinite(cursorId)) {
      conds.push(gt(whatsappIdentities.id, cursorId));
    }
    if (filter.source === "dm") {
      conds.push(eq(whatsappContacts.seenInDm, 1));
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

    // source=group / a specific group => restrict to lids present as members.
    const groupScoped = filter.source === "group" || Boolean(filter.group);

    const baseSelect = {
      identityId: whatsappIdentities.id,
      lid: whatsappIdentities.lid,
      phoneNumber: whatsappIdentities.phoneNumber,
      name: whatsappIdentities.name,
      businessName: whatsappIdentities.businessName,
      seenInDm: whatsappContacts.seenInDm,
    };

    let rows: IdentityRow[];

    if (groupScoped) {
      const groupConds = [...conds];
      if (filter.group) {
        groupConds.push(eq(whatsappGroupMembers.groupJid, filter.group));
      }
      rows = await db
        .select(baseSelect)
        .from(whatsappContacts)
        .innerJoin(
          whatsappIdentities,
          eq(whatsappIdentities.lid, whatsappContacts.lid),
        )
        .innerJoin(
          whatsappGroupMembers,
          and(
            eq(whatsappGroupMembers.sessionId, whatsappContacts.sessionId),
            eq(whatsappGroupMembers.lid, whatsappContacts.lid),
          ),
        )
        .where(and(...groupConds))
        .groupBy(whatsappIdentities.id)
        .orderBy(asc(whatsappIdentities.id))
        .limit(PAGE_LIMIT + 1);
    } else {
      rows = await db
        .select(baseSelect)
        .from(whatsappContacts)
        .innerJoin(
          whatsappIdentities,
          eq(whatsappIdentities.lid, whatsappContacts.lid),
        )
        .where(and(...conds))
        .orderBy(asc(whatsappIdentities.id))
        .limit(PAGE_LIMIT + 1);
    }

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
    const {
      whatsappContacts,
      whatsappIdentities,
      whatsappGroupMembers,
      whatsappGroups,
    } = await import("~/lib/db/wa");
    const { and, eq } = await import("drizzle-orm");

    const idRows = await db
      .select({
        identityId: whatsappIdentities.id,
        lid: whatsappIdentities.lid,
        phoneNumber: whatsappIdentities.phoneNumber,
        name: whatsappIdentities.name,
        businessName: whatsappIdentities.businessName,
        seenInDm: whatsappContacts.seenInDm,
      })
      .from(whatsappIdentities)
      .leftJoin(
        whatsappContacts,
        and(
          eq(whatsappContacts.lid, whatsappIdentities.lid),
          eq(whatsappContacts.sessionId, sessionId),
        ),
      )
      .where(eq(whatsappIdentities.lid, lid))
      .limit(1);

    const idRow = idRows[0];
    if (!idRow) return null;

    const groupRows = await db
      .select({
        jid: whatsappGroupMembers.groupJid,
        name: whatsappGroups.subject,
        nickname: whatsappGroupMembers.groupNickname,
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
      identity: rowToContact(idRow),
      dm: Boolean(idRow.seenInDm),
      groups: groupRows.map((g) => ({
        jid: g.jid,
        name: g.name ?? undefined,
        nickname: g.nickname ?? undefined,
        role: g.role,
        lastSeen: g.lastSeen ?? undefined,
      })),
    };
  });

// ===== row -> DTO mappers (mirror the gateway's REST response shapes) =====

type IdentityRow = {
  identityId: number;
  lid: string;
  phoneNumber: string | null;
  name: string | null;
  businessName: string | null;
  seenInDm: number | null;
};

function rowToContact(r: IdentityRow): Contact {
  // The schema has no separate push-name column; surface the business name as
  // the push name fallback (the gateway REST does the same enrichment).
  return {
    lid: r.lid,
    phoneNumber: r.phoneNumber ?? undefined,
    name: r.name ?? undefined,
    pushName: r.businessName ?? undefined,
    source: r.seenInDm ? "dm" : "group",
  };
}
