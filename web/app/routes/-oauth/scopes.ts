// Plain-language descriptions of the OIDC scopes an app may request, for the
// consent card (oauth.md §7.6). End-users are not developers — phrase each as a
// concrete thing the app will learn, not the raw scope token.

interface ScopeInfo {
  /** Short label shown in the list. */
  label: string;
  /** One-line plain description. */
  description: string;
}

const SCOPES: Record<string, ScopeInfo> = {
  openid: {
    label: "Confirm it's you",
    description: "That you control this WhatsApp number.",
  },
  profile: {
    label: "Your WhatsApp name",
    description: "The display name on your WhatsApp profile.",
  },
  phone: {
    label: "Your phone number",
    description: "Your WhatsApp phone number.",
  },
  "wa:group": {
    label: "Your group membership",
    description: "That you're a member of the app's group.",
  },
  offline_access: {
    label: "Stay signed in",
    description: "Keep you signed in without asking again each time.",
  },
};

export interface ScopeLine {
  key: string;
  label: string;
  description: string;
}

/** Map raw scope tokens to display lines. `openid` is the base "confirm it's
 * you" line; unknown scopes fall back to the raw token so nothing is hidden. */
export function describeScopes(scopes: string[]): ScopeLine[] {
  return scopes.map((key) => {
    const info = SCOPES[key];
    return info
      ? { key, label: info.label, description: info.description }
      : { key, label: key, description: "Additional access requested by the app." };
  });
}
