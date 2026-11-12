import { describe, expect, it } from "vitest";
import { countdown, relativeTime } from "./agentTime";
import { fromWireAgentToken } from "./agentWire";

const NOW = Date.parse("2026-11-11T10:00:00Z");

describe("relativeTime (#1351)", () => {
  it("names the nearest whole unit, ahead and behind", () => {
    expect(relativeTime("2027-01-26T10:00:00Z", NOW)).toBe("in 76 days");
    expect(relativeTime("2026-09-29T10:00:00Z", NOW)).toBe("43 days ago");
    expect(relativeTime("2026-11-11T13:00:00Z", NOW)).toBe("in 3 hours");
    expect(relativeTime("2026-11-11T10:00:30Z", NOW)).toBe("in 1 minute");
  });
});

describe("countdown (#1351)", () => {
  it("reads minutes and seconds, and never goes negative", () => {
    expect(countdown("2026-11-11T10:00:41Z", NOW)).toBe("0:41");
    expect(countdown("2026-11-11T10:14:03Z", NOW)).toBe("14:03");
    expect(countdown("2026-11-11T09:00:00Z", NOW)).toBe("0:00");
  });
});

describe("a token row (#1351)", () => {
  it("carries the prefix and never a hash or a secret", () => {
    const row = fromWireAgentToken({
      id: "tok_01", name: "nightly-triage", prefix: "rat_q7Hn", scope: "read", owner: "admin", status: "active",
      created_at: "2026-10-28T00:00:00Z", expires_at: "2027-01-26T00:00:00Z", last_used_at: null, revoked_at: null
    });
    expect(Object.keys(row).sort()).toEqual([
      "createdAt", "expiresAt", "id", "lastUsedAt", "name", "owner", "prefix", "revokeCommand", "revokedAt", "scope", "status"
    ]);
    expect(row.revokeCommand).toBeNull();
  });
});
