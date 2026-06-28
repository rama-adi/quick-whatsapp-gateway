// Domain types re-exported from the generated OpenAPI schema. No hand-typed
// models — the generated schema is the single source of truth.
// FROZEN — owned by the foundation agent. Surface agents import, never edit.

import type { components } from "./schema";

export type WASession = components["schemas"]["WASession"];
export type BackfillJob = components["schemas"]["BackfillJob"];
export type BackfillImport = components["schemas"]["BackfillImport"];
export type CreateSessionRequest = components["schemas"]["CreateSessionInputBody"];
export type SessionMe = components["schemas"]["Me"]; // WA own profile (NOT actor)
export type QRCode = components["schemas"]["QR"];

export type Message = components["schemas"]["Message"];
export type SendRequest = components["schemas"]["SendRequest"];
export type SendResult = components["schemas"]["SendResult"];
export type ContactCard = components["schemas"]["ContactCard"];

export type Chat = components["schemas"]["Chat"];
export type UpdateChatRequest = components["schemas"]["UpdateChatInputBody"];

export type Contact = components["schemas"]["Contact"];
export type ContactDetail = components["schemas"]["ContactDetail"];
export type OnWhatsApp = components["schemas"]["OnWhatsApp"];
export type ProfilePicture = components["schemas"]["ProfilePicture"];

export type Group = components["schemas"]["GroupInfo"];
export type GroupMember = components["schemas"]["GroupMember"];

// API-key management moved to better-auth on the frontend (the gateway only
// *verifies* keys). No gateway /keys schemas remain — see app/routes/_app/user/keys.tsx.

export type Webhook = components["schemas"]["Webhook"];
export type WebhookRequest = components["schemas"]["WebhookBody"];
export type RetryPolicy = components["schemas"]["RetryPolicy"];

// The event envelope is now a typed, discriminated union — one member per event
// type, generated from the gateway's Go event catalog (the OpenAPI `webhooks`
// section). Consumers switch on `event` and get the matching `payload` shape.
export type EventEnvelope =
  | components["schemas"]["MessageEvent"]
  | components["schemas"]["MessageStatusEvent"]
  | components["schemas"]["SessionStatusEvent"]
  | components["schemas"]["AuthQREvent"]
  | components["schemas"]["AuthCodeEvent"]
  | components["schemas"]["PresenceEvent"]
  | components["schemas"]["GroupEvent"]
  | components["schemas"]["ChatUpdateEvent"]
  | components["schemas"]["ContactUpdateEvent"]
  | components["schemas"]["CallEvent"]
  | components["schemas"]["NewsletterEvent"];

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
