/**
 * Issue #1504: the Dashboard's activity section runs one backup set into the
 * next, so it is hard to tell which terminal log belongs to which set, and
 * the Assurance list is always open.
 *
 * What I want:
 *  - each set's activity is its own block, with its own edge and clear space
 *    between it and the next one;
 *  - each log says which set it belongs to, in words, so a log scrolled into
 *    view on its own can still be attributed;
 *  - the Assurance list is collapsible and collapsed by default, with a
 *    header that still says how many backups need attention.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const strips = (p: Page) => p.getByRole("region", { name: /^Activity for / });
const assurance = (p: Page) => p.getByRole("region", { name: "Assurance summary" });
const dueList = (p: Page) => assurance(p).getByRole("list", { name: "Backups needing assurance" });
const toggle = (p: Page) => assurance(p).getByRole("button", { name: /^assurance/i });

async function opened(p: Page) {
  await open(p, "/");
  // The strips arrive with the first read of the feed.
  await expect(strips(p).nth(1)).toBeVisible();
}

test.describe("#1504 separation between backup sets on the Dashboard", () => {
  test("#1504 each set's activity is its own block with an edge on every side", async ({ page }) => {
    await opened(page);
    const n = await strips(page).count();
    for (let i = 0; i < n; i++) {
      const edges = await strips(page).nth(i).evaluate((el) => {
        const cs = getComputedStyle(el);
        return [cs.borderTopWidth, cs.borderRightWidth, cs.borderBottomWidth, cs.borderLeftWidth].map((w) => parseFloat(w));
      });
      for (const w of edges) expect(w).toBeGreaterThanOrEqual(1);
    }
  });

  test("#1504 there is clear space between one set's block and the next", async ({ page }) => {
    await opened(page);
    const n = await strips(page).count();
    for (let i = 1; i < n; i++) {
      const above = (await strips(page).nth(i - 1).boundingBox())!;
      const below = (await strips(page).nth(i).boundingBox())!;
      expect(below.y - (above.y + above.height)).toBeGreaterThanOrEqual(12);
    }
  });

  test("#1504 each set's log names the set it belongs to", async ({ page }) => {
    await opened(page);
    const n = await strips(page).count();
    for (let i = 0; i < n; i++) {
      const strip = strips(page).nth(i);
      const name = ((await strip.getAttribute("aria-label")) ?? "").replace(/^Activity for /, "");
      // The log itself is named for the set, for a screen reader...
      await expect(strip.getByRole("log")).toHaveAccessibleName(new RegExp(name));
      // ...and a caption says so on screen, inside the strip, above the lines.
      await expect(strip.getByText(new RegExp("terminal log for .*" + name, "i")).first()).toBeVisible();
    }
  });
});

test.describe("#1504 the Assurance list collapses", () => {
  test("#1504 it is collapsed by default, and the header still says how many need attention", async ({ page }) => {
    await opened(page);
    await expect(toggle(page)).toHaveAttribute("aria-expanded", "false");
    await expect(dueList(page)).toBeHidden();
    await expect(assurance(page)).toContainText(/3 backups? need/i);
  });

  test("#1504 pressing the header opens the list and pressing it again closes it", async ({ page }) => {
    await opened(page);
    await toggle(page).click();
    await expect(toggle(page)).toHaveAttribute("aria-expanded", "true");
    await expect(dueList(page).getByRole("listitem")).toHaveCount(3);
    await toggle(page).click();
    await expect(toggle(page)).toHaveAttribute("aria-expanded", "false");
    await expect(dueList(page)).toBeHidden();
  });

  test("#1504 the Due, Overdue and Degraded counts stay visible while the list is collapsed", async ({ page }) => {
    await opened(page);
    await expect(assurance(page).getByTestId("assurance-degraded")).toBeVisible();
    await expect(assurance(page).getByTestId("assurance-due")).toBeVisible();
  });
});
