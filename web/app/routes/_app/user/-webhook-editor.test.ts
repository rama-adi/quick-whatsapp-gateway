import { describe, expect, it } from "vitest";
import {
  emptyForm,
  formFromWebhook,
  toRequest,
  validateWebhookForm,
} from "./-webhook-editor";
import type { Webhook } from "~/lib/api/types";

describe("webhook editor", () => {
  it("omits a blank write-only secret when editing", () => {
    const state = formFromWebhook({
      id: "wh_1",
      url: "https://example.com/hook",
      events: ["message"],
      active: true,
    } as Webhook);

    expect(toRequest(state)).not.toHaveProperty("secret");
  });

  it("rejects duplicate header names case-insensitively", () => {
    const state = {
      ...emptyForm(),
      url: "https://example.com/hook",
      headers: [
        { key: "Authorization", value: "one" },
        { key: "authorization", value: "two" },
      ],
    };

    expect(validateWebhookForm(state)).toBe(
      "Custom header names must be unique.",
    );
  });

  it("rejects integrity headers controlled by the gateway", () => {
    const state = {
      ...emptyForm(),
      url: "https://example.com/hook",
      headers: [{ key: "X-Webhook-Signature", value: "spoofed" }],
    };

    expect(validateWebhookForm(state)).toBe(
      "Content-Type and X-Webhook-* headers are managed by the gateway.",
    );
  });
});
