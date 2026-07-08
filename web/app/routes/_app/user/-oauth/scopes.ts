// Developer-facing scope catalog for the OAuth-app editor checklist (oauth.md
// §7.6). These descriptions are for the ORG DEVELOPER configuring the app — not
// the end-user (that copy lives in routes/-oauth/scopes.ts, consumed by the
// consent card). Same tokens, different audience.

export interface DashScope {
  key: string;
  label: string;
  /** What the relying app gets, in developer terms. */
  description: string;
  /** openid is mandatory (OIDC base) — rendered checked + locked. */
  required?: boolean;
}

export const DASH_SCOPES: DashScope[] = [
  {
    key: "openid",
    label: "openid",
    description: "Required. Issues an ID token with the user's WhatsApp LID subject (sub).",
    required: true,
  },
  {
    key: "profile",
    label: "profile",
    description: "The user's WhatsApp display name (name claim).",
  },
  {
    key: "phone",
    label: "phone",
    description:
      "The verified phone number in E.164 (phone_number, phone_number_verified, wa_jid).",
  },
  {
    key: "wa:group",
    label: "wa:group",
    description:
      "Proof of membership in the pinned group (wa_group_verified, wa_group_id, wa_group_name). Group mode only.",
  },
  {
    key: "offline_access",
    label: "offline_access",
    description: "Issues a rotating refresh token so the app can stay signed in.",
  },
];

export const ALL_SCOPE_KEYS = DASH_SCOPES.map((s) => s.key);
