import { describe, expect, it } from "vitest";
import { resolveTheme } from "@shared/theme";
import { graph, resetGraphForTests } from "@shared/state/graph";
import { tooltipsEnabledNode } from "@shared/state/tooltipNodes";

/**
 * Issue #1542, the file that goes last. scripts/shared-fork-1542.test.mjs
 * runs it in the same vitest fork as two files that leave state behind (the
 * opt-out file, then registry-tooltips, whose last case turns tooltips off),
 * so by
 * the time this file loads, everything a file can leave outside its own
 * modules has been left.
 *
 * Read at load, before any case here runs, because what matters is what
 * this file's modules saw as they were imported: tooltipNodes seeds its
 * node from storage at import, and resetGraphForTests() goes back to that
 * seed rather than to the default.
 */
const atLoad = {
  localStorage: window.localStorage.length,
  sessionStorage: window.sessionStorage.length,
  rootTheme: document.documentElement.getAttribute("data-theme"),
  theme: resolveTheme(),
  tooltipsEnabled: graph.read(tooltipsEnabledNode)
};

describe("a file that shares a fork with files that left state behind (#1542)", () => {
  it("starts with nothing stored, no theme on the root and tooltips on", () => {
    expect(atLoad).toEqual({
      localStorage: 0,
      sessionStorage: 0,
      rootTheme: null,
      theme: "light",
      tooltipsEnabled: true
    });
  });

  it("resets the tooltip preference to on, not to whatever was stored at import", () => {
    resetGraphForTests();
    expect(graph.read(tooltipsEnabledNode)).toBe(true);
  });
});
