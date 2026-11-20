import { describe, expect, it } from "vitest";
import { applyTheme } from "@shared/theme";
import { graph } from "@shared/state/graph";
import {
  TOOLTIPS_KEY,
  TOOLTIP_PROMPTED_KEY,
  setTooltipsEnabled,
  tooltipsEnabledNode
} from "@shared/state/tooltipNodes";

/**
 * Issue #1542, the file that goes first. scripts/shared-fork-1542.test.mjs
 * runs this, then registry-tooltips.test.tsx, then
 * shared-fork-1542-starts-clean, all in one vitest fork. That is how the two registry-tooltips failures on MARS
 * came about: one file opted out of tooltips, the next file imported
 * tooltipNodes, and the node was seeded "off" from what the first file had
 * left in storage.
 *
 * So this file does exactly that on purpose. It opts out, marks the prompt
 * as asked, drops something in sessionStorage, renders the theme dark, and
 * cleans none of it up. Whatever runs after it in the same fork has to
 * start clean anyway.
 */
describe("a file that opts out of tooltips and leaves it that way (#1542)", () => {
  it("opts out, and does not clean up after itself", () => {
    setTooltipsEnabled(false);
    window.localStorage.setItem(TOOLTIP_PROMPTED_KEY, "1");
    window.sessionStorage.setItem("retnd.shared-fork-1542", "left behind");
    applyTheme("dark");

    expect(window.localStorage.getItem(TOOLTIPS_KEY)).toBe("off");
    expect(graph.read(tooltipsEnabledNode)).toBe(false);
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
  });
});
