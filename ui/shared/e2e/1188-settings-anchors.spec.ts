import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, setTheme } from "./helpers";

/**
 * Issue #1188: a link into Settings lands on its section, not at a random spot.
 * The Default configuration chip is the report; every Settings section gets a
 * stable anchor id from one table (src/pages/settingsSections.ts).
 */
const S = "assurance-default-untested";
const NEWEST = "postgres-prod-20260829.dump.zst";
const IDS = [
  "service-behaviour", "backup-defaults", "capacity", "retention", "storage-destinations", "workflow", "assurance",
  "notifications", "interface", "platform", "system-information", "password", "account-recovery"
];
const chip = (page: Page) =>
  page.getByRole("row").filter({ has: page.locator("td.mono", { hasText: NEWEST }) }).locator('[data-claim="recovery"]')
    .getByRole("link", { name: "Default configuration" });
/** Opens Settings with the hash after the query, the order a browser puts them in. */
async function openAt(page: Page, hash: string) {
  await open(page, "/settings", S, { wire: true });
  await page.goto("/settings?scenario=" + S + "&wire=1" + hash);
}

test.describe("#1188 the Default configuration chip lands on the Assurance section", () => {
  test("#1188 clicking the chip puts the Assurance heading in view, just below the top of the page", async ({ page }) => {
    await open(page, "/backups", S, { wire: true });
    await chip(page).click();
    await expect(page).toHaveURL(/\/settings#assurance$/);
    const heading = page.locator("#assurance").getByRole("heading", { name: "Assurance" });
    await expect(heading).toBeInViewport({ ratio: 1 });
    // Let the cards below it load: the heading must still be where it landed.
    await page.waitForTimeout(1500);
    await expect(heading).toBeInViewport({ ratio: 1 });
    const top = (await heading.boundingBox())!.y;
    expect(top).toBeGreaterThanOrEqual(0);
    expect(top).toBeLessThan(160);
  });

  test("#1188 opening /settings#assurance directly lands there too", async ({ page }) => {
    await openAt(page, "#assurance");
    await page.waitForTimeout(1500);
    await expect(page.locator("#assurance").getByRole("heading", { name: "Assurance" })).toBeInViewport({ ratio: 1 });
  });
});

test.describe("#1188 every Settings section has a stable anchor", () => {
  test("#1188 each id in the table exists exactly once on Settings", async ({ page }) => {
    await open(page, "/settings", S, { wire: true });
    await expect(page.locator("#assurance")).toHaveCount(1);
    for (const id of IDS) await expect(page.locator("#" + id), id).toHaveCount(1);
  });

  test("#1188 a link to another section (#storage-destinations) lands on its heading", async ({ page }) => {
    await openAt(page, "#storage-destinations");
    await page.waitForTimeout(1500);
    const el = page.locator("#storage-destinations");
    await expect(el).toBeInViewport();
    const top = (await el.boundingBox())!.y;
    expect(top).toBeGreaterThanOrEqual(0);
    expect(top).toBeLessThan(160);
  });
});

test.describe("#1188 the anchor styling", () => {
  test("#1188 targets keep clear of the top edge", async ({ page }) => {
    await open(page, "/settings", S, { wire: true });
    await expect(page.locator("#assurance")).toHaveCount(1);
    const margin = await page.locator("#assurance").evaluate((el) => parseFloat(getComputedStyle(el).scrollMarginTop));
    expect(margin).toBeGreaterThanOrEqual(12);
  });

  test("#1188 scrolling is smooth only when the reader has not asked for reduced motion", async ({ page }) => {
    const behaviour = () => page.evaluate(() => getComputedStyle(document.documentElement).scrollBehavior);
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await open(page, "/settings", S, { wire: true });
    await expect(page.locator("#assurance")).toHaveCount(1);
    expect(await behaviour()).toBe("smooth");
    await page.emulateMedia({ reducedMotion: "reduce" });
    expect(await behaviour()).toBe("auto");
  });

  for (const theme of ["light", "dark"] as const) {
    test(`#1188 the :target highlight is visible and meets 3:1 against the page in the ${theme} theme`, async ({ page }) => {
      await openAt(page, "#assurance");
      await setTheme(page, theme);
      const card = page.locator("#assurance");
      await expect(card).toBeVisible();
      const m = await page.evaluate(() => {
        const el = document.querySelector("#assurance")!;
        const cs = getComputedStyle(el);
        const c = document.createElement("canvas").getContext("2d")!;
        const rgb = (s: string) => { c.fillStyle = "#000"; c.fillStyle = s; c.fillRect(0, 0, 1, 1); return Array.from(c.getImageData(0, 0, 1, 1).data.slice(0, 3)); };
        const lum = ([r, g, b]: number[]) => { const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b); };
        let bgEl: Element | null = el.parentElement; let bg = "rgba(0, 0, 0, 0)";
        while (bgEl && /rgba\(0, 0, 0, 0\)|transparent/.test(bg)) { bg = getComputedStyle(bgEl).backgroundColor; bgEl = bgEl.parentElement; }
        const ring = cs.outlineStyle !== "none" && parseFloat(cs.outlineWidth) > 0 ? cs.outlineColor : "";
        if (!ring) return { ring: "", ratio: 0 };
        const a = lum(rgb(ring)), b = lum(rgb(bg));
        return { ring, ratio: (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05) };
      });
      console.log(`CONTRAST #1188 :target outline, ${theme} | ${m.ratio.toFixed(2)}:1 (needs 3) | ${m.ring}`);
      expect(m.ring).not.toBe("");
      expect(m.ratio).toBeGreaterThanOrEqual(3);
    });
  }
});
