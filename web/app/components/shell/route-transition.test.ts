import { describe, expect, it } from "vitest";
import { routeTransitionKey } from "./route-transition";

describe("routeTransitionKey", () => {
  it("keys ordinary pages by pathname", () => {
    expect(routeTransitionKey("/user/keys")).toBe("/user/keys");
    expect(routeTransitionKey("/user/keys/")).toBe("/user/keys");
    expect(routeTransitionKey("/admin/gateways/gw-1")).toBe("/admin/gateways/gw-1");
    expect(routeTransitionKey("/")).toBe("/");
  });

  it("animates between session sub-surfaces", () => {
    expect(routeTransitionKey("/user/sessions/s1")).toBe("/user/sessions/s1");
    expect(routeTransitionKey("/user/sessions/s1/chats")).not.toBe(
      routeTransitionKey("/user/sessions/s1"),
    );
    expect(routeTransitionKey("/user/sessions/s1/contacts")).not.toBe(
      routeTransitionKey("/user/sessions/s1/chats"),
    );
  });

  it("does not re-animate when switching the open chat or contact", () => {
    const chats = routeTransitionKey("/user/sessions/s1/chats");
    expect(routeTransitionKey("/user/sessions/s1/chats/")).toBe(chats);
    expect(routeTransitionKey("/user/sessions/s1/chats/123%40s.whatsapp.net")).toBe(chats);
    expect(routeTransitionKey("/user/sessions/s1/chats/456%40g.us")).toBe(chats);

    const contacts = routeTransitionKey("/user/sessions/s1/contacts");
    expect(routeTransitionKey("/user/sessions/s1/contacts/lid-9")).toBe(contacts);
  });

  it("does not treat look-alike segments as stable surfaces", () => {
    expect(routeTransitionKey("/user/sessions/s1/chatsx")).toBe(
      "/user/sessions/s1/chatsx",
    );
  });
});
