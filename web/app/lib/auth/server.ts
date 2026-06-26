// better-auth SERVER config (server-only) — the identity core of the v2 frontend
// (§4, §12). Mounted at /api/auth/* (see app/routes/api/auth.$.ts).
//
// Owns the auth tables via the Drizzle adapter (§6.2 single-writer):
// user/session/account/verification/jwks/apikey/organization/member/invitation/
// twoFactor/admin. Generated schema lives in app/lib/db/auth-schema.ts
// (`pnpm auth:generate`).
//
// GATEWAY CONTRACT honored here (load-bearing — the gateway verifies against these):
//   - JWT: asymmetric EdDSA/Ed25519 (the plugin default — we DO NOT override alg),
//     iss==aud==BETTER_AUTH_URL, 5-min expiry, JWKS at /api/auth/jwks, token at
//     /api/auth/token. definePayload adds activeOrganizationId + orgRole (active-org
//     member role) + role (platform admin role). sub defaults to user id.
//   - api-key: DEFAULT hash scheme = base64url(SHA-256(rawKey)) unpadded — we leave
//     disableKeyHashing=false and DO NOT pass a custom hasher. Keys are ORG-SCOPED
//     via `references: "organization"`: the key's `referenceId` column = the org id,
//     and `organizationId` is REQUIRED on create. The gateway resolves the owning
//     org by reading apikey.referenceId. permissions map to {read,send,manage,events}.
//   - Control bus: api-key delete -> ctrl:apikey.revoked {keyId}; user ban ->
//     ctrl:user.banned {userId}; org member removal -> ctrl:member.removed
//     {userId, organizationId}. Published via app/lib/server/control-bus.ts.

import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { createAuthMiddleware } from "better-auth/api";
import { admin } from "better-auth/plugins/admin";
import { adminAc, userAc } from "better-auth/plugins/admin/access";
import { jwt } from "better-auth/plugins/jwt";
import { organization } from "better-auth/plugins/organization";
import { twoFactor } from "better-auth/plugins/two-factor";
import { apiKey } from "@better-auth/api-key";
import { and, eq } from "drizzle-orm";
import { db } from "~/lib/db";
import { member } from "~/lib/db/auth-schema";
import {
  publishApiKeyRevoked,
  publishMemberRemoved,
  publishUserBanned,
} from "~/lib/server/control-bus";

const BASE_URL = process.env.BETTER_AUTH_URL ?? "http://localhost:3000";

// Self-registration gate (§12, §14). Default ON to match the spec default; set
// USER_REGISTRATION_ENABLED=false to lock down sign-up.
const REGISTRATION_ENABLED = process.env.USER_REGISTRATION_ENABLED !== "false";

// Trusted origins for CSRF / origin checks (§14). Comma-list; always include
// our own base URL.
const trustedOrigins = (process.env.TRUSTED_ORIGINS ?? "")
  .split(",")
  .map((s) => s.trim())
  .filter(Boolean);
if (!trustedOrigins.includes(BASE_URL)) trustedOrigins.push(BASE_URL);

/** Look up a user's active-org member role for the JWT's `orgRole` claim. */
async function lookupOrgRole(
  userId: string,
  organizationId: string,
): Promise<string | null> {
  try {
    const rows = await db
      .select({ role: member.role })
      .from(member)
      .where(and(eq(member.userId, userId), eq(member.organizationId, organizationId)))
      .limit(1);
    return rows[0]?.role ?? null;
  } catch (err) {
    console.error("[auth] orgRole lookup failed:", err);
    return null;
  }
}

/** A user's first org membership — used to seed a session's active org. */
async function firstMembership(
  userId: string,
): Promise<{ organizationId: string; role: string } | null> {
  try {
    const rows = await db
      .select({ organizationId: member.organizationId, role: member.role })
      .from(member)
      .where(eq(member.userId, userId))
      .limit(1);
    return rows[0] ?? null;
  } catch (err) {
    console.error("[auth] firstMembership lookup failed:", err);
    return null;
  }
}

export const auth = betterAuth({
  appName: "WA Gateway",
  baseURL: BASE_URL, // also reads BETTER_AUTH_URL; explicit keeps iss/aud stable.
  // secret comes from BETTER_AUTH_SECRET env (better-auth reads it automatically).
  trustedOrigins,

  database: drizzleAdapter(db, { provider: "mysql" }),

  emailAndPassword: {
    enabled: true,
    // Block self sign-up when registration is disabled; admin-created users still work.
    disableSignUp: !REGISTRATION_ENABLED,
  },

  databaseHooks: {
    user: {
      create: {
        // A personal organization is auto-created per user on signup. better-auth
        // has no built-in "personal org"; we create it server-side here. (Setting
        // it active happens in session.create.before, since no session exists yet
        // at user-create time during sign-up.)
        after: async (user) => {
          try {
            const slug = `personal-${user.id}`.toLowerCase().slice(0, 48);
            await auth.api.createOrganization({
              body: {
                name: user.name ? `${user.name}'s Org` : "Personal",
                slug,
                userId: user.id, // creator = this user (server call, no session)
                keepCurrentActiveOrganization: false,
              },
            });
          } catch (err) {
            // Non-fatal: signup must still succeed. The user can create/switch
            // orgs from the UI; the active-org claim is just empty until then.
            console.error("[auth] personal-org auto-create failed:", err);
          }
        },
      },
    },
    session: {
      create: {
        // Seed the active org onto every new session so definePayload always has
        // an activeOrganizationId to emit (better-auth doesn't auto-set it on
        // login). Picks the user's first membership (their personal org for a
        // fresh signup); the org switcher can change it later.
        before: async (session) => {
          if ((session as { activeOrganizationId?: string }).activeOrganizationId) {
            return;
          }
          const m = await firstMembership(session.userId);
          if (!m) return;
          return { data: { ...session, activeOrganizationId: m.organizationId } };
        },
      },
    },
  },

  plugins: [
    twoFactor(),

    admin({
      // Platform roles surfaced in the JWT as "role". super_admin = cross-org
      // oversight. adminRoles must be keys of `roles`, so we define super_admin
      // (full admin access) + user (default) using the admin plugin's access control.
      roles: { super_admin: adminAc, user: userAc },
      defaultRole: "user",
      adminRoles: ["super_admin"],
      // Bootstrap platform admins by user id (comma-list, §12). Optional.
      adminUserIds: (process.env.ADMIN_USER_IDS ?? "")
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean),
    }),

    apiKey({
      // ORG-SCOPED keys: referenceId column = the active organization id.
      // organizationId is REQUIRED on create; the gateway reads referenceId to
      // resolve the owning org (see header + RISK note in the receipt).
      references: "organization",
      // DEFAULT hashing (base64url(SHA-256), unpadded) — gateway replicates it.
      // disableKeyHashing stays false; no custom hasher.
      enableMetadata: true,
      defaultPrefix: "wa_",
      permissions: {
        // {read,send,manage,events} mapped onto a single resource bucket; the
        // gateway checks these scopes per action (§4.3).
        defaultPermissions: { gateway: ["read"] },
      },
    }),

    jwt({
      jwks: {
        // Asymmetric EdDSA/Ed25519 is the plugin default; stated explicitly for clarity.
        keyPairConfig: { alg: "EdDSA", crv: "Ed25519" },
      },
      jwt: {
        issuer: BASE_URL,
        audience: BASE_URL,
        expirationTime: "5m",
        definePayload: async ({ user, session }) => {
          const activeOrganizationId =
            (session as { activeOrganizationId?: string | null })
              .activeOrganizationId ?? null;
          const orgRole = activeOrganizationId
            ? await lookupOrgRole(user.id, activeOrganizationId)
            : null;
          return {
            // sub defaults to user.id (getSubject default).
            activeOrganizationId,
            // orgRole: owner|admin|member for the active org.
            orgRole,
            // role = the admin-plugin PLATFORM role (e.g. super_admin).
            role: (user as { role?: string }).role ?? "user",
          };
        },
      },
    }),

    organization({
      // The personal org's creator gets "owner"; org roles gate within-org access.
      creatorRole: "owner",
      // Email invitations (§12). Stub logger for now — real email lands later.
      async sendInvitationEmail(data) {
        console.info(
          `[auth] invitation for ${data.email} to org ${data.organization.name} ` +
            `(id=${data.id}) — wire a real mailer here.`,
        );
      },
    }),
  ],

  hooks: {
    // Control-bus publishes ride better-auth `after` hooks (§4.6). Matched by
    // ctx.path; published only when the endpoint succeeded (the after hook only
    // runs past a thrown APIError).
    after: createAuthMiddleware(async (ctx) => {
      const path = ctx.path;

      if (path === "/api-key/delete") {
        const keyId = (ctx.body as { keyId?: string } | undefined)?.keyId;
        if (keyId) await publishApiKeyRevoked(keyId);
        return;
      }

      if (path === "/admin/ban-user") {
        const userId = (ctx.body as { userId?: string } | undefined)?.userId;
        if (userId) await publishUserBanned(userId);
        return;
      }

      if (path === "/organization/remove-member") {
        // remove-member returns the removed member ({ member: { userId, organizationId }}).
        const returned = ctx.context.returned as
          | { member?: { userId?: string; organizationId?: string } }
          | undefined;
        const userId = returned?.member?.userId;
        const organizationId =
          returned?.member?.organizationId ??
          (ctx.body as { organizationId?: string } | undefined)?.organizationId;
        if (userId && organizationId) {
          await publishMemberRemoved(userId, organizationId);
        }
        return;
      }
    }),
  },
});

export type Auth = typeof auth;
