import { describe, expect, it } from "vitest";
import { MOCK_MINTED_SECRET } from "./mockAgents";

/**
 * Q8 (#1367): the token the mock hands out must not be one the engine could
 * accept, so a copy of it in a screenshot or a pasted log opens nothing.
 *
 * The rule mirrored here is the shape core/service/agenttokens.go mints:
 * agentTokenPrefix ("rat_") plus base64.RawURLEncoding of 32 random bytes,
 * which is 43 characters of [A-Za-z0-9_-], 47 in all. AuthenticateAgentToken
 * looks a bearer up by its first agentTokenPrefixLen (8) characters and
 * compares the hash of the whole string, so a string of any other shape can
 * never match a row and is refused as AGENT_TOKEN_INVALID.
 */
const MINTED_SHAPE = /^rat_[A-Za-z0-9_-]{43}$/;

describe("MOCK_MINTED_SECRET (Q8, #1367)", () => {
  it("is the documented stand-in", () => {
    expect(MOCK_MINTED_SECRET).toBe("rat_EXAMPLE0000000000000000000000000000000000");
  });

  it("carries the rat_ prefix, so it is routed to agent authentication, and fails the minted shape, so it is refused there", () => {
    expect(MOCK_MINTED_SECRET.startsWith("rat_")).toBe(true);
    expect(MOCK_MINTED_SECRET.length).toBeGreaterThanOrEqual(8);
    expect(MINTED_SHAPE.test(MOCK_MINTED_SECRET)).toBe(false);
  });

  it("the shape rule accepts a token of the length the engine mints", () => {
    expect(MINTED_SHAPE.test("rat_" + "A".repeat(43))).toBe(true);
  });
});
