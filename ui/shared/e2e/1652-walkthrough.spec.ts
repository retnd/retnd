/**
 * Slow pass over the Backups table for the GIF in #1652 and its PR. At 1280px
 * the table is scrolled to its far left, where a ruler draws the gap from the
 * card's edge to the row checkbox, then to its far right, where a second ruler
 * draws the gap from the Actions button to the card's edge. A strip along the
 * bottom prints both numbers. Light, then dark. Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, setTheme } from "./helpers";

test.describe.configure({ timeout: 150_000 });
test.use({ viewport: { width: 1280, height: 900 } });
const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);

/** Test-only overlay: a bar from the card edge to the control, with its width in px. */
async function ruler(page: Page, side: "left" | "right", label: string, other: string) {
  await page.evaluate(([s, text, rest]) => {
    document.querySelectorAll("[data-gap-ruler]").forEach((e) => e.remove());
    const card = document.querySelector(".card .table")!.closest(".card")!.getBoundingClientRect();
    const target = s === "left"
      ? document.querySelector<HTMLElement>("tbody td.selcell input")!.getBoundingClientRect()
      : document.querySelector<HTMLElement>("tbody td.actcell button")!.getBoundingClientRect();
    const x1 = s === "left" ? card.left : target.right;
    const x2 = s === "left" ? target.left : card.right;
    const y = target.top + target.height / 2;
    const bar = document.createElement("div");
    bar.setAttribute("data-gap-ruler", "1");
    bar.style.cssText = "position:fixed;z-index:99998;background:#d11a2a;height:6px;left:" + x1 + "px;width:" + Math.max(x2 - x1, 2) + "px;top:" + (y - 3) + "px";
    const tag = document.createElement("div");
    tag.setAttribute("data-gap-ruler", "1");
    tag.textContent = (x2 - x1).toFixed(1) + " px";
    tag.style.cssText = "position:fixed;z-index:99998;font:700 15px ui-monospace,monospace;color:#fff;background:#d11a2a;padding:2px 6px;border-radius:4px;top:" + (y - 34) + "px;left:" + (s === "left" ? x1 : x2 - 90) + "px";
    const strip = document.createElement("div");
    strip.setAttribute("data-gap-ruler", "1");
    strip.textContent = text + " | " + rest;
    strip.style.cssText = "position:fixed;left:0;right:0;bottom:0;z-index:99999;padding:10px 16px;font:600 15px/1.3 ui-monospace,monospace;background:#111;color:#fff;border-top:3px solid #d11a2a";
    document.body.append(bar, tag, strip);
  }, [side, label, other]);
}

async function gaps(page: Page) {
  return page.evaluate(() => {
    const sc = document.querySelector(".card .table-scroll")!;
    const card = sc.closest(".card")!.getBoundingClientRect();
    sc.scrollLeft = 0;
    const l = document.querySelector<HTMLElement>("tbody td.selcell input")!.getBoundingClientRect().left - card.left;
    sc.scrollLeft = sc.scrollWidth;
    const r = card.right - document.querySelector<HTMLElement>("tbody td.actcell button")!.getBoundingClientRect().right;
    sc.scrollLeft = 0;
    return { l, r };
  });
}

test("#1652 walkthrough: Backups table, left gap then right gap, light then dark", async ({ page }) => {
  await open(page, "/backups", "backups-many-artifact-sets");
  await expect(page.locator(".card .table tbody tr").nth(2)).toBeVisible({ timeout: 30_000 });
  for (const theme of ["light", "dark"] as const) {
    await page.evaluate(() => document.querySelectorAll("[data-gap-ruler]").forEach((e) => e.remove()));
    await setTheme(page, theme);
    const { l, r } = await gaps(page);
    const verdict = theme + " | left " + l.toFixed(1) + " px, right " + r.toFixed(1) + " px, expected equal";
    const sc = page.locator(".card .table-scroll").first();
    await sc.evaluate((e) => { e.scrollLeft = 0; });
    await page.locator(".card").first().scrollIntoViewIfNeeded();
    await beat(page, 800);
    await ruler(page, "left", "LEFT: card edge to the row checkbox", verdict);
    await beat(page, 2200);
    await page.evaluate(() => document.querySelectorAll("[data-gap-ruler]").forEach((e) => e.remove()));
    for (const f of [0.25, 0.5, 0.75, 1]) {
      await sc.evaluate((e, k) => { e.scrollLeft = (e.scrollWidth - e.clientWidth) * k; }, f);
      await beat(page, 450);
    }
    await ruler(page, "right", "RIGHT: Actions button to the card edge", verdict);
    await beat(page, 2600);
  }
});
