import { describe, expect, it } from "vitest";
import { validateWAIntrospectionGrants } from "../../../scripts/wa-introspection-grants.mts";

const allowed = new Set(["gateways", "wa_sessions"]);
const usage = "GRANT USAGE ON *.* TO `wa_reader`@`%`";
const valid = [usage, "GRANT SELECT ON `gateway`.`gateways` TO `wa_reader`@`%`", "GRANT SELECT ON `gateway`.`wa_sessions` TO `wa_reader`@`%`"];
const rejects = [
  ["column list", "GRANT SELECT (`id`) ON `gateway`.`gateways` TO `wa_reader`@`%`"],
  ["role", "GRANT `reader_role`@`%` TO `wa_reader`@`%`"],
  ["proxy", "GRANT PROXY ON `root`@`%` TO `wa_reader`@`%`"],
  ["global select", "GRANT SELECT ON *.* TO `wa_reader`@`%`"],
  ["schema select", "GRANT SELECT ON `gateway`.* TO `wa_reader`@`%`"],
  ["other database", "GRANT SELECT ON `other`.`gateways` TO `wa_reader`@`%`"],
  ["extra table", "GRANT SELECT ON `gateway`.`audit_events` TO `wa_reader`@`%`"],
  ["multi privilege", "GRANT SELECT, INSERT ON `gateway`.`gateways` TO `wa_reader`@`%`"],
  ["file", "GRANT FILE ON *.* TO `wa_reader`@`%`"],
  ["process", "GRANT PROCESS ON *.* TO `wa_reader`@`%`"],
  ["super", "GRANT SUPER ON *.* TO `wa_reader`@`%`"],
  ["reload", "GRANT RELOAD ON *.* TO `wa_reader`@`%`"],
  ["shutdown", "GRANT SHUTDOWN ON *.* TO `wa_reader`@`%`"],
  ["show databases", "GRANT SHOW DATABASES ON *.* TO `wa_reader`@`%`"],
  ["grant option", "GRANT SELECT ON `gateway`.`gateways` TO `wa_reader`@`%` WITH GRANT OPTION"],
] as const;

describe("WA introspection grants", () => {
  it("accepts exact quoted grants and handles identifier case", () => {
    expect(() => validateWAIntrospectionGrants("GateWay", allowed, valid)).not.toThrow();
    expect(() => validateWAIntrospectionGrants("gateway", allowed, [usage, "GRANT SELECT ON gateway.GATEWAYS TO u@h", "GRANT SELECT ON gateway.WA_SESSIONS TO u@h"])).not.toThrow();
  });
  it.each(rejects)("rejects %s", (_name, bad) => {
    const rows = bad.includes("gateways") ? [usage, bad, valid[2]!] : [...valid, bad];
    expect(() => validateWAIntrospectionGrants("gateway", allowed, rows)).toThrow();
  });
  it("rejects missing, duplicate, or repeated USAGE grants", () => {
    expect(() => validateWAIntrospectionGrants("gateway", allowed, valid.slice(0, 2))).toThrow();
    expect(() => validateWAIntrospectionGrants("gateway", allowed, [...valid, valid[1]!])).toThrow();
    expect(() => validateWAIntrospectionGrants("gateway", allowed, [usage, ...valid])).toThrow();
  });
});
