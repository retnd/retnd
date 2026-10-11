/**
 * Issue #985: the claim model, the feed merge and the inert log, plus the
 * tooltip contract for the new controls (preference explicitly ON).
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@shared/design-system/components.css";
import { ClaimChips } from "@shared/assurance/ClaimChips";
import { renderClaim, renderStored, tally } from "@shared/assurance/claims";
import { inert } from "@shared/assurance/inert";
import { mergeBySeq } from "@shared/assurance/useOperation";
import type { AchievedClass, ClaimStatus } from "@shared/assurance/types";
import { resetGraphForTests } from "@shared/state/graph";
import { TOOLTIPS_KEY, TOOLTIP_PROMPTED_KEY, setTooltipsEnabled } from "@shared/state/tooltipNodes";
import { TOOLTIPS } from "@shared/tooltips/tooltips";

const NOW = Date.parse("2026-10-10T12:00:00Z");
const ago = (days: number) => new Date(NOW - days * 86_400_000).toISOString();
const STATUSES: ClaimStatus[] = [
  "unknown", "never_configured", "never_run", "pass", "overdue", "refused_for_cost",
  "could_not_complete", "failed", "requires_restore", "no_readable_copy"
];
const CLASSES: AchievedClass[] = ["", "content", "attested", "existence"];

describe("a pass is derived from the record, never carried by it", () => {
  it("renders no content pass without the content class and a time, across every combination", () => {
    for (const status of STATUSES) {
      for (const achievedClass of CLASSES) {
        for (const at of [undefined, "not a time", ago(3)]) {
          const r = renderClaim("content", { status, achievedClass, at }, NOW);
          const earned = status === "pass" && achievedClass === "content" && at === ago(3);
          expect(r.state === "pass", `${status}/${achievedClass}/${at}`).toBe(earned);
        }
      }
    }
  });

  it("renders no recovery pass without a time", () => {
    expect(renderClaim("recovery", { status: "pass", achievedClass: "content" }, NOW).state).toBe("unknown");
    expect(renderClaim("recovery", { status: "pass", achievedClass: "", at: ago(1) }, NOW).state).toBe("pass");
  });

  it("an absent record, and a status this build does not know, are unknown", () => {
    expect(renderClaim("content", undefined, NOW).state).toBe("unknown");
    expect(renderClaim("recovery", undefined, NOW).state).toBe("unknown");
    expect(renderClaim("content", { status: "verified-ish" as ClaimStatus, achievedClass: "content", at: ago(1) }, NOW).state).toBe("unknown");
  });

  it("an existence or attested result is never content verification", () => {
    const e = renderClaim("content", { status: "pass", achievedClass: "existence", at: ago(1) }, NOW);
    const a = renderClaim("content", { status: "pass", achievedClass: "attested", at: ago(1) }, NOW);
    expect(e.state).toBe("existence_only");
    expect(a.state).toBe("attested_only");
    expect(e.label).not.toMatch(/verified/i);
    expect(a.label).not.toMatch(/verified/i);
  });

  it("every state has its own glyph and its own words", () => {
    const seen = new Set<string>();
    const words = new Set<string>();
    for (const status of STATUSES.filter((s) => s !== "pass")) {
      const r = renderClaim("content", { status, achievedClass: "content", at: ago(2) }, NOW);
      seen.add(r.glyph.id);
      words.add(r.label);
    }
    expect(seen.size).toBe(9);
    expect(words.size).toBe(9);
  });

  it("stored is a fact about copies and never a pass", () => {
    expect(renderStored(0).state).toBe("no_copy");
    expect(renderStored(undefined).state).toBe("unknown");
    expect(renderStored(2).label).toBe("Stored · 2 copies");
  });

  it("unknown is due", () => {
    const u = { status: "unknown" as ClaimStatus, achievedClass: "" as AchievedClass };
    expect(tally([{ content: u, recovery: u }], NOW).due).toBe(1);
  });
});

describe("the live feed merges by sequence", () => {
  it("a replay from the start adds nothing the reader already holds", () => {
    const ev = (seq: number) => ({ seq, at: "", stage: "read", text: "line " + seq });
    const held = [ev(1), ev(2), ev(3)];
    expect(mergeBySeq(held, [ev(1), ev(2), ev(3)])).toBe(held);
    expect(mergeBySeq(held, [ev(2), ev(3), ev(4)]).map((e) => e.seq)).toEqual([1, 2, 3, 4]);
    expect(mergeBySeq([ev(4)], [ev(2), ev(1)]).map((e) => e.seq)).toEqual([1, 2, 4]);
  });
});

describe("validator output is displayed inert", () => {
  it("replaces every control and window sequence with its visible escape", () => {
    expect(inert("a\u001b]0;t\u0007b")).toBe("a\\x1b]0;t\\x07b");
    expect(inert("\u001b[2J\u009b31m")).toBe("\\x1b[2J\\x9b31m");
    expect(inert("keeps\nnewlines\tand tabs")).toBe("keeps\nnewlines\tand tabs");
    expect(inert("‮rtl")).toBe("\\u202ertl");
  });
});

describe("the claim chips and the tooltip registry, preference explicitly ON", () => {
  beforeEach(() => {
    resetGraphForTests();
    localStorage.setItem(TOOLTIPS_KEY, "on");
    localStorage.setItem(TOOLTIP_PROMPTED_KEY, "1");
    setTooltipsEnabled(true);
  });
  afterEach(() => { cleanup(); localStorage.clear(); });

  it("each chip keeps its own accessible name and is also described by its registry entry", () => {
    render(
      <ClaimChips
        assurance={{
          artifactId: "a", copies: 1,
          content: { status: "failed", achievedClass: "", at: new Date().toISOString() },
          recovery: { status: "never_run", achievedClass: "" }
        }}
      />
    );
    const content = screen.getByRole("group", { name: /^Content: Content check failed/ });
    expect(content).toBeTruthy();
    const claim = content.closest("[data-claim]") as HTMLElement;
    expect(claim.getAttribute("aria-describedby")).toMatch(/tip/);
    expect(screen.getByRole("group", { name: "Recovery: Never run" })).toBeTruthy();
  });

  it("every assurance entry uses only the allowed tags", () => {
    for (const [id, entry] of Object.entries(TOOLTIPS)) {
      if (!id.startsWith("assurance.")) continue;
      const tags = [...entry.html.matchAll(/<\/?([a-z]+)/gi)].map((m) => m[1].toLowerCase());
      for (const t of tags) expect(["strong", "em", "code", "br"], id).toContain(t);
    }
  });

  it("the host-scoped readouts and the strategy select are refused an entry, on purpose", () => {
    const ids = Object.keys(TOOLTIPS).filter((id) => id.startsWith("assurance."));
    for (const refused of ["assurance.host.stagingRoot", "assurance.host.stagingTtl", "assurance.host.concurrency", "assurance.host.memoryBudget", "assurance.set.strategy"]) {
      expect(ids).not.toContain(refused);
    }
  });
});
