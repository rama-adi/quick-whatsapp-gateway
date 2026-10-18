// Developer-facing scope catalog for the OAuth-app editor checklist (oauth.md
// §7.6). These descriptions are for the ORG DEVELOPER configuring the app — not
// the end-user (that copy lives in routes/-oauth/scopes.ts, consumed by the
// consent card). Same tokens, different audience.

export interface DashScope {
  key: string;
  /** The OAuth scope token, as sent by the app. */
  label: string;
  /** Plain-language name for the scope. */
  title: string;
  /** What the relying app gets, in developer terms. */
  description: string;
  /** openid is mandatory (OIDC base) — rendered checked + locked. */
  required?: boolean;
}

export const DASH_SCOPES: DashScope[] = [
  {
    key: "openid",
    label: "openid",
    title: "Sign-in identity",
    description: "Always on. A stable user ID (the sub claim) your app can store, plus an ID token.",
    required: true,
  },
  {
    key: "profile",
    label: "profile",
    title: "Display name",
    description: "The user's WhatsApp display name (name claim).",
  },
  {
    key: "phone",
    label: "phone",
    title: "Phone number",
    description:
      "The verified phone number, e.g. +628123456789 (phone_number, phone_number_verified, wa_jid).",
  },
  {
    key: "wa:group",
    label: "wa:group",
    title: "Group membership",
    description:
      "Confirms the user is in your sign-in group (wa_group_verified, wa_group_id, wa_group_name). Needs group verification.",
  },
  {
    key: "offline_access",
    label: "offline_access",
    title: "Stay signed in",
    description: "A refresh token, so your app can keep the user signed in without asking again.",
  },
];

export const ALL_SCOPE_KEYS = DASH_SCOPES.map((s) => s.key);

/** Plain-language name for a scope token ("phone" → "Phone number"). */
export function scopeTitle(key: string): string {
  return DASH_SCOPES.find((s) => s.key === key)?.title ?? key;
}
