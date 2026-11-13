import { describe, expect, it } from "vitest";
import golden from "../../../../core/cliecho/testdata/cli_outcomes.golden?raw";
import { auditCommand, mintCommand, revokeCommand } from "./agentCommands";

/**
 * The three twins the browser composes (Q13, #1367) are held to the CLI here.
 * The strings are what core/cliecho/routes.go builds for the same inputs (the
 * flags in its order: `agent token create NAME --scope --expires-in`,
 * `agent token revoke ID`, `agent audit --token --since`), and the golden is
 * asserted to still list each route with a command for the example the
 * builder was copied from, so a route that loses its twin turns this red.
 */
const exampleIsACommand = (route: string, example: string) => {
  const block = golden.split("\n\n").find((b) => b.startsWith(route + "\n"));
  expect(block, route + " is in cli_outcomes.golden").toBeDefined();
  expect(block!.split("\n")).toContain("\t" + example + ": command");
};

describe("agentCommands (Q13, #1367)", () => {
  it("mintCommand equals the create line cliecho echoes", () => {
    exampleIsACommand("POST /agent/tokens", 'example {"name":"triage","scope":"read","expires_in":"30d"}');
    expect(mintCommand({ name: "triage", scope: "read", expiresIn: "30d" })).toBe("retnd agent token create triage --scope read --expires-in 30d");
  });

  it("mintCommand leaves out a scope or an expiry that is empty, as cliecho does", () => {
    exampleIsACommand("POST /agent/tokens", 'example {"name":"triage"}');
    expect(mintCommand({ name: "triage", scope: "", expiresIn: "" })).toBe("retnd agent token create triage");
  });

  it("mintCommand quotes a name the shell would split", () => {
    expect(mintCommand({ name: "a b", scope: "read", expiresIn: "90d" })).toBe("retnd agent token create 'a b' --scope read --expires-in 90d");
  });

  it("revokeCommand equals the revoke line cliecho echoes", () => {
    exampleIsACommand("POST /agent/tokens/{id}/revoke", "example");
    expect(revokeCommand("agt_0123456789abcdef")).toBe("retnd agent token revoke agt_0123456789abcdef");
  });

  it("auditCommand equals the audit line cliecho echoes, with only the filters set", () => {
    exampleIsACommand("GET /agent/audit", "example");
    expect(auditCommand({})).toBe("retnd agent audit");
    expect(auditCommand({ tokenId: "agt_0123456789abcdef", since: "7d" })).toBe("retnd agent audit --token agt_0123456789abcdef --since 7d");
    expect(auditCommand({ since: "24h" })).toBe("retnd agent audit --since 24h");
  });
});
