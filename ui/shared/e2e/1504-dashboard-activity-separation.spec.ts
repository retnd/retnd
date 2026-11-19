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
import { measureContrast, open, setTheme } from "./helpers";

const strips = (p: Page) => p.getByRole("region", { name: /^Activity for / });
const assurance = (p: Page) => p.getByRole("region", { name: "Assurance summary" });
const dueList = (p: Page) => assurance(p).getByRole("list", { name: "Backups needing assurance" });
const toggle = (p: Page) => assurance(p).getByRole("button", { name: /^(show|hide) \d+ backups?$/i });

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

test.describe("#1504 each set's name opens its details page", () => {
  test("#1504 the name is a link to the set's page, built the way every other link to it is", async ({ page }) => {
    await opened(page);
    const link = strips(page).first().getByRole("link");
    await expect(link).toHaveAttribute("href", /^\/sets\/[^/]+\/[^/]+$/);
    await link.click();
    await expect(page).toHaveURL(/\/sets\/[^/]+\/[^/]+$/);
  });

  test("#1504 the link's accessible name is its visible text, so voice control can say it", async ({ page }) => {
    await opened(page);
    const link = strips(page).first().getByRole("link");
    await expect(link).toHaveAccessibleName((await link.textContent())!.trim());
  });

  test("#1504 a set's own page keeps its look: no card edge and no caption repeating its header", async ({ page }) => {
    await open(page, "/sets/production/postgres-primary");
    const strip = strips(page).first();
    await expect(strip).toBeVisible();
    expect(await strip.evaluate((el) => parseFloat(getComputedStyle(el).borderTopWidth))).toBe(0);
    await expect(strip.getByText(/^Terminal log for /)).toHaveCount(0);
    // The log is still named for the set, for a screen reader.
    await expect(strip.getByRole("log")).toHaveAccessibleName(/Terminal log for /);
  });
});

test.describe("#1504 the Assurance list collapses", () => {
  test("#1504 it is collapsed by default, and the toggle says how many backups are behind it", async ({ page }) => {
    await opened(page);
    await expect(toggle(page)).toHaveAttribute("aria-expanded", "false");
    await expect(toggle(page)).toHaveText("Show 3 backups");
    await expect(dueList(page)).toBeHidden();
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

test.describe("#1504 contrast of what this adds (WCAG AA)", () => {
  for (const theme of ["light", "dark"] as const) {
    test("#1504 the card edge is 3:1 against the card and the page, and the text is 4.5:1, in the " + theme + " theme", async ({ page }) => {
      await opened(page);
      await setTheme(page, theme);
      const edge = await strips(page).first().evaluate((el) => {
        const lum = (c: string) => {
          const cv = document.createElement("canvas").getContext("2d")!;
          cv.fillStyle = "#000";
          cv.fillStyle = c;
          cv.fillRect(0, 0, 1, 1);
          const d = cv.getImageData(0, 0, 1, 1).data;
          const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
          return 0.2126 * f(d[0]) + 0.7152 * f(d[1]) + 0.0722 * f(d[2]);
        };
        const ratio = (a: string, b: string) => { const [x, y] = [lum(a), lum(b)].sort((m, n) => n - m); return (x + 0.05) / (y + 0.05); };
        const root = getComputedStyle(document.documentElement);
        const edgeColour = getComputedStyle(el).borderTopColor;
        return {
          onCard: ratio(edgeColour, getComputedStyle(el).backgroundColor),
          onPage: ratio(edgeColour, root.getPropertyValue("--bg").trim())
        };
      });
      console.log("CONTRAST #1504 card edge, " + theme + " | on card " + edge.onCard.toFixed(2) + ":1, on page " + edge.onPage.toFixed(2) + ":1 (needs 3)");
      expect(edge.onCard).toBeGreaterThanOrEqual(3);
      expect(edge.onPage).toBeGreaterThanOrEqual(3);
      expect(await measureContrast(page, "1504 strips " + theme, '[aria-label^="Activity for "]')).toEqual([]);
      expect(await measureContrast(page, "1504 assurance " + theme, '[aria-label="Assurance summary"]')).toEqual([]);
    });
  }
});
