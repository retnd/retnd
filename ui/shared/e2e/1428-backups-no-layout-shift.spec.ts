import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

// Issue #1428: /backups drew its rows at one height and, a moment later when
// the assurance claims arrived, every row grew and the page got far taller
// under the reader (and under any script scrolling to the bottom). The
// Dashboard's assurance card did the same. The claims have to hold the shape
// they will have while they load.
declare global {
  interface Window { __shifts: { h: number; probe: number }[] }
}

/** Records the page height and the probe element's height at every frame in
 *  which either changes, from the first frame the probe exists. */
async function track(page: Page, probe: string) {
  await page.addInitScript((sel) => {
    window.__shifts = [];
    const tick = () => {
      const el = document.querySelector(sel);
      if (el) {
        const h = document.documentElement.scrollHeight;
        const p = Math.round(el.getBoundingClientRect().height);
        const last = window.__shifts[window.__shifts.length - 1];
        if (!last || last.h !== h || last.probe !== p) window.__shifts.push({ h, probe: p });
      }
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  }, probe);
}

const cases = [
  { path: "/backups", name: "Backups", probe: "tbody tr", chips: '[data-claim="content"]' },
  { path: "/", name: "Dashboard assurance card", probe: 'section[aria-label="Assurance summary"]', chips: '[data-testid="assurance-due"] a, [data-testid="assurance-due"] button' }
] as const;

for (const c of cases) {
  test(`#1428 ${c.name}: does not change height when the claims arrive`, async ({ page }) => {
    await track(page, c.probe);
    await open(page, c.path, "default", { wire: true });
    await expect(page.locator(c.chips).first()).toBeVisible();
    await page.waitForTimeout(2500);
    const s = await page.evaluate(() => window.__shifts);
    const first = s[0];
    const last = s[s.length - 1];
    console.log(`#1428 ${c.path} first draw: page ${first.h}px, probe ${first.probe}px; settled: page ${last.h}px, probe ${last.probe}px; ${s.length} layout states`);
    // The Dashboard page has other cards that load on their own schedule, so
    // only the Backups page is held to a flat page height.
    if (c.path === "/backups") expect(Math.max(...s.map((x) => x.h)) - Math.min(...s.map((x) => x.h)), "page height range across the load").toBeLessThanOrEqual(1);
    expect(last.probe, "height of the first row / card after the claims load").toBe(first.probe);
  });
}
