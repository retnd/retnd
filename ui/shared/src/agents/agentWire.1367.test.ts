import { describe, expect, it } from "vitest";
import { fromWireAgentAudit, mintRefusalField } from "./agentWire";

describe("mintRefusalField (Q16, #1367)", () => {
  it.each([
    ["name", "name: must be 1 to 40 lowercase letters, digits or hyphens."],
    ["expires_in", "expires_in: may not exceed 365d."],
    ["scope", "scope: must be read, operate or configure."]
  ])("routes a message that starts with %s: to its field", (field, message) => {
    expect(mintRefusalField(message)).toBe(field);
  });

  it("falls back to the form for anything that does not start with a field and a colon", () => {
    expect(mintRefusalField("an active token named x already exists.")).toBeNull();
    expect(mintRefusalField("names must be short")).toBeNull();
    expect(mintRefusalField("expires_in must be a duration")).toBeNull();
    expect(mintRefusalField("see name: above")).toBeNull();
    expect(mintRefusalField("")).toBeNull();
  });
});

describe("fromWireAgentAudit token_name (Q15, #1367)", () => {
  const entry = { arguments: "{}", at: "2026-11-11T10:00:00Z", code: "", confirmation_id: "", correlation_id: "c1", seq: 1, status: 200, token_id: "agt_1", tool: "status", transport: "stdio" };

  it("reads the name the engine joined, and leaves it empty for a row with no token", () => {
    const page = fromWireAgentAudit({ entries: [{ ...entry, token_name: "triage" }, { ...entry, seq: 2, token_id: "", token_name: "" }, { ...entry, seq: 3 }] });
    expect(page.rows.map((r) => r.tokenName)).toEqual(["triage", "", ""]);
  });
});
