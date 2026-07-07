// Build a mock PendingSnapshot from the current editor form so the live consent
// preview reuses the real end-user ConsentCard (oauth.md §6.2). Same wire shape
// the router serves at authorize time — the preview is faithful, not a mock-up.

import type { PendingSnapshot, WaitTarget } from "~/routes/-oauth/protocol";
import type { OAuthMode } from "~/lib/api/hooks/oauth";

export interface PreviewInput {
  name: string;
  logoUrl?: string;
  loginCommand: string;
  scopes: string[];
  modes: OAuthMode[];
  /** Human-readable bot number (from the bound session), if paired. */
  botNumber?: string;
  /** Pinned group subject, when group mode is on. */
  groupName?: string;
}

/** A fixed sample code so the preview is stable (never a real code). */
const SAMPLE_USER_CODE = "483920";

export function buildPreviewSnapshot(input: PreviewInput): PendingSnapshot {
  // Prefer DM in the preview when enabled (the default acr, oauth.md §4.3),
  // else group.
  const preferDm = input.modes.includes("dm") || input.modes.length === 0;
  const target: WaitTarget = preferDm
    ? {
        mode: "dm",
        number: input.botNumber || "+62 812-0000-0000",
        bot_name: input.name || undefined,
      }
    : {
        mode: "group",
        group_name: input.groupName || "Your pinned group",
        number: input.botNumber || undefined,
        bot_name: input.name || undefined,
      };

  return {
    status: "pending",
    app: {
      name: input.name.trim() || "Your app",
      logo: input.logoUrl?.trim() || null,
    },
    user_code: SAMPLE_USER_CODE,
    login_command: input.loginCommand.trim() || "login",
    target,
    scopes: input.scopes.length ? input.scopes : ["openid"],
    // Fixed 10-minute countdown so the preview shows the timer without ticking
    // toward a real expiry.
    expires_at: Date.now() + 600_000,
  };
}
