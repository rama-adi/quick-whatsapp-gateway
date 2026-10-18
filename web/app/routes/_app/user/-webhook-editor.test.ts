import { describe, expect, it } from "vitest";
import {
  emptyForm,
  formFromWebhook,
  generateSecret,
  lastRetryAfterSeconds,
  toRequest,
  validateWebhookForm,
  webhookFieldErrors,
} from "./-webhook-editor";
import type { Webhook } from "~/lib/api/types";

const hook = (over: Partial<Webhook> = {}): Webhook =>
  ({
    id: "wh_1",
    url: "https://example.com/hook",
    events: ["message"],
    active: true,
    retryPolicy: { attempts: 5, delaySeconds: 10, policy: "constant" },
    ...over,
  }) as Webhook;

describe("webhook editor", () => {
  it("omits a blank write-only secret when editing", () => {
    expect(toRequest(formFromWebhook(hook()))).not.toHaveProperty("secret");
  });

  it("does not send a typed secret on edit unless replacing it", () => {
    const state = { ...formFromWebhook(hook()), secret: "whsec_x" };
    expect(toRequest(state)).not.toHaveProperty("secret");
    expect(toRequest({ ...state, replaceSecret: true }).secret).toBe("whsec_x");
  });

  it("sends the secret on create", () => {
    const state = { ...emptyForm(), url: "https://example.com/h", secret: "s3" };
    expect(toRequest(state).secret).toBe("s3");
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

  it("reports errors per field", () => {
    const errors = webhookFieldErrors({
      ...emptyForm(),
      url: "not a url",
      allEvents: false,
    });
    expect(errors.url).toBeDefined();
    expect(errors.events).toBeDefined();
    expect(errors.headers).toBeUndefined();
  });

  it("omits retryPolicy on create unless customized", () => {
    const base = { ...emptyForm(), url: "https://example.com/h" };
    expect(toRequest(base)).not.toHaveProperty("retryPolicy");
    expect(
      toRequest({
        ...base,
        customRetry: true,
        retry: { attempts: "3", delaySeconds: "5", policy: "constant" },
      }).retryPolicy,
    ).toEqual({ attempts: 3, delaySeconds: 5, policy: "constant" });
  });

  it("round-trips an existing retry policy on edit", () => {
    expect(toRequest(formFromWebhook(hook())).retryPolicy).toEqual({
      attempts: 5,
      delaySeconds: 10,
      policy: "constant",
    });
  });

  it("validates retry numbers", () => {
    const errors = webhookFieldErrors({
      ...emptyForm(),
      url: "https://example.com/h",
      customRetry: true,
      retry: { attempts: "", delaySeconds: "0", policy: "exponential" },
    });
    expect(errors.attempts).toBeDefined();
    expect(errors.delaySeconds).toBeDefined();
  });

  it("keeps an empty event list as 'none', not 'all'", () => {
    const state = formFromWebhook(hook({ events: [] }));
    expect(state.allEvents).toBe(false);
    expect(validateWebhookForm(state)).toBe(
      "Select at least one event, or choose all events.",
    );
  });

  it("computes when the last retry happens", () => {
    expect(
      lastRetryAfterSeconds({ attempts: "15", delaySeconds: "2", policy: "exponential" }),
    ).toBe(2 * (2 ** 14 - 1));
    expect(
      lastRetryAfterSeconds({ attempts: "4", delaySeconds: "10", policy: "constant" }),
    ).toBe(30);
    expect(
      lastRetryAfterSeconds({ attempts: "x", delaySeconds: "10", policy: "constant" }),
    ).toBeNull();
  });

  it("generates distinct prefixed secrets", () => {
    const a = generateSecret();
    expect(a).toMatch(/^whsec_[0-9a-f]{64}$/);
    expect(generateSecret()).not.toBe(a);
  });
});
