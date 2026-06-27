// Domain types re-exported from the generated OpenAPI schema. No hand-typed
// models — the generated schema is the single source of truth.
// FROZEN — owned by the foundation agent. Surface agents import, never edit.

import type { components } from "./schema";

export type WASession = components["schemas"]["WASession"];
export type BackfillJob = components["schemas"]["BackfillJob"];
export type CreateSessionRequest = components["schemas"]["CreateSessionRequest"];
export type SessionMe = components["schemas"]["SessionMe"]; // WA own profile (NOT actor)
export type QRCode = components["schemas"]["QRCode"];

export type Message = components["schemas"]["Message"];
export type SendRequest = components["schemas"]["SendRequest"];
export type SendResult = components["schemas"]["SendResult"];
export type ContactCard = components["schemas"]["ContactCard"];

export type Chat = components["schemas"]["Chat"];
export type UpdateChatRequest = components["schemas"]["UpdateChatRequest"];

export type Contact = components["schemas"]["Contact"];
export type ContactDetail = components["schemas"]["ContactDetail"];
export type OnWhatsApp = components["schemas"]["OnWhatsApp"];
export type ProfilePicture = components["schemas"]["ProfilePicture"];

export type Group = components["schemas"]["GroupInfo"];
export type GroupMember = components["schemas"]["GroupMember"];
export type GroupSettings = components["schemas"]["GroupSettings"];

// API-key management moved to better-auth on the frontend (the gateway only
// *verifies* keys). No gateway /keys schemas remain — see app/routes/_app/user/keys.tsx.

export type Webhook = components["schemas"]["Webhook"];
export type WebhookRequest = components["schemas"]["WebhookRequest"];
export type RetryPolicy = components["schemas"]["RetryPolicy"];

export type EventEnvelope = components["schemas"]["Event"];

/** Session lifecycle status union (starting|scan_qr_code|working|failed|stopped|logged_out). */
export type SessionStatus = NonNullable<WASession["status"]>;

/** Message delivery status union (pending|sent|delivered|read|played|failed). */
export type MessageStatus = NonNullable<Message["status"]>;

/** Send type discriminator (text|poll|location|contact|image|...). */
export type SendType = NonNullable<SendRequest["type"]>;

/** Lifecycle actions exposed via the :start/:stop/:restart/:logout endpoints. */
export type SessionAction = "start" | "stop" | "restart" | "logout";

/** Contact list filter that feeds the contacts query key + URL params. */
export type ContactFilter = {
  source?: "dm" | "group";
  group?: string;
  q?: string;
};
