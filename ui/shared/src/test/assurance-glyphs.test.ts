/**
 * Issue #988: the glyph guard that could not fail.
 *
 * `assurance.test.tsx` checks that every not-a-pass state has its own glyph,
 * and it filters the pass out before it counts, so a glyph table in which
 * `unknown` carried the pass glyph passed it, and so did every Playwright spec
 * over the claims. That is the one mutation the whole epic exists to prevent
 * (a thing nobody looked at drawn as a tick), found by planting it
 * (scripts/bdtools/assurance/ui_selftest.py) and watching nothing go red.
 *
 * These two cases count the pass too, and say the one thing that must hold
 * directly.
 */
import { describe, expect, it } from "vitest";
import { GLYPH, renderClaim } from "@shared/assurance/claims";

const NOW = Date.parse("2026-10-10T12:00:00Z");

describe("no claim state borrows another state's glyph", () => {
  it("every state, the pass included, has a glyph id no other state has", () => {
    const ids = Object.values(GLYPH).map((g) => g.id);
    expect(new Set(ids).size, "two states share a glyph: " + ids.join(", ")).toBe(ids.length);
  });

  it("a claim nobody looked at is never drawn with the pass glyph, in either claim", () => {
    for (const kind of ["content", "recovery"] as const) {
      for (const record of [undefined, { status: "unknown", achievedClass: "", at: undefined }] as const) {
        const r = renderClaim(kind, record as never, NOW);
        expect(r.state, kind).toBe("unknown");
        expect(r.glyph.id, kind).not.toBe(GLYPH.pass.id);
        expect(r.glyph.icon, kind).not.toBe(GLYPH.pass.icon);
      }
    }
  });
});
