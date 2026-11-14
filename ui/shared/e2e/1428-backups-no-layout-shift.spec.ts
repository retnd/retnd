import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

// Issue #1428: /backups drew its rows at one height and, a moment later when
// the assurance claims arrived, every row grew and the page got far taller
// under the reader (and under any script scrolling to the bottom). The
// Dashboard's assurance card did the same. The claims have to hold the shape
// they will have while they load.
declare global {
  interface Window { __shifts: { h: number; probe: number; top: number }[] }
}

/** Records the page height, the probe element's height and the top of
 *  `below` (what sits under it) at every frame in which any changes, from the
 *  first frame the probe or `below` exists. */
async function track(page: Page, probe: string, below: string) {
  await page.addInitScript(([sel, under]) => {
    window.__shifts = [];
    const tick = () => {
      const el = document.querySelector(sel) ?? document.querySelector(under);
      if (el) {
        const h = document.documentElement.scrollHeight;
        const p = Math.round((document.querySelector(sel) ?? el).getBoundingClientRect().height);
        const u = document.querySelector(under);
        const top = u ? Math.round(u.getBoundingClientRect().top + window.scrollY) : -1;
        const last = window.__shifts[window.__shifts.length - 1];
        if (!last || last.h !== h || last.probe !== p || last.top !== top) window.__shifts.push({ h, probe: p, top });
      }
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  }, [probe, below]);
}

const cases = [
  { path: "/backups", name: "Backups", probe: "tbody tr", chips: '[data-claim="content"]', below: "tbody tr:last-child" },
  { path: "/", name: "Dashboard assurance card", probe: 'section[aria-label="Assurance summary"]', chips: '[data-testid="assurance-due"] a, [data-testid="assurance-due"] button', below: 'section[aria-label="Recent activity"]' }
] as const;

for (const c of cases) {
  test(`#1428 ${c.name}: nothing moves when the claims arrive`, async ({ page }) => {
    await track(page, c.probe, c.below);
    await open(page, c.path, "default", { wire: true });
    await expect(page.locator(c.chips).first()).toBeVisible();
    await page.waitForTimeout(2500);
    const s = await page.evaluate(() => window.__shifts);
    const first = s[0];
    const last = s[s.length - 1];
    console.log(`#1428 ${c.path} first draw: page ${first.h}px, probe ${first.probe}px, below top ${first.top}px; settled: page ${last.h}px, probe ${last.probe}px, below top ${last.top}px; ${s.length} layout states`);
    // The Dashboard page has other cards that load on their own schedule, so
    // only the Backups page is held to a flat page height.
    if (c.path === "/backups") expect(Math.max(...s.map((x) => x.h)) - Math.min(...s.map((x) => x.h)), "page height range across the load").toBeLessThanOrEqual(1);
    if (c.path === "/backups") expect(last.probe, "height of the first row after the claims load").toBe(first.probe);
    // Whatever sits under it (the last row, or Recent activity) must not move
    // once it has been drawn. -1 is "not drawn yet", which is not a move.
    const tops = s.map((x) => x.top).filter((t) => t >= 0);
    expect(Math.max(...tops) - Math.min(...tops), "movement of what sits below").toBeLessThanOrEqual(1);
  });
}
