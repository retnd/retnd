/**
 * Slow pass over a backup set's page for the GIF in #1235 and in its PR: the
 * text drawn with --text-3 is outlined, and a strip along the bottom prints
 * the live ratio of --text-3 against the card (--surface) and the page
 * (--bg), first in the light theme and then in the dark one. Run with
 * E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, setTheme } from "./helpers";

const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);
test.describe.configure({ timeout: 150_000 });

/** Outlines every element whose own text is in --text-3, and writes the
 *  measured ratios into a fixed strip. Test-only; nothing here ships. */
async function annotate(page: Page, theme: string) {
  const n = await page.evaluate((th) => {
    const c = document.createElement("canvas").getContext("2d")!;
    const rgb = (s: string) => { c.fillStyle = "#000"; c.fillStyle = s; c.fillRect(0, 0, 1, 1); const d = c.getImageData(0, 0, 1, 1).data; return [d[0], d[1], d[2]]; };
    const lum = (a: number[]) => { const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }; return 0.2126 * f(a[0]) + 0.7152 * f(a[1]) + 0.0722 * f(a[2]); };
    const ratio = (a: number[], b: number[]) => { const x = lum(a), y = lum(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
    const root = getComputedStyle(document.documentElement);
    const t3 = rgb(root.getPropertyValue("--text-3"));
    const onCard = ratio(t3, rgb(root.getPropertyValue("--surface")));
    const onPage = ratio(t3, rgb(root.getPropertyValue("--bg")));
    document.querySelectorAll("[data-t3-mark]").forEach((e) => { (e as HTMLElement).style.outline = ""; e.removeAttribute("data-t3-mark"); });
    let count = 0;
    document.querySelectorAll("body *").forEach((e) => {
      const own = [...e.childNodes].some((x) => x.nodeType === 3 && (x.textContent ?? "").trim());
      if (!own || e.closest("[data-t3-strip]")) return;
      if (rgb(getComputedStyle(e).color).join() !== t3.join()) return;
      const h = e as HTMLElement;
      h.style.outline = "2px dashed #d11a2a";
      h.style.outlineOffset = "1px";
      h.setAttribute("data-t3-mark", "1");
      count++;
    });
    document.querySelector("[data-t3-strip]")?.remove();
    const strip = document.createElement("div");
    strip.setAttribute("data-t3-strip", "1");
    const pass = (r: number) => (r >= 4.5 ? "PASS" : "FAIL");
    strip.textContent = th + " theme | --text-3 text: " + onCard.toFixed(2) + ":1 on card " + pass(onCard) +
      ", " + onPage.toFixed(2) + ":1 on page " + pass(onPage) + " (needs 4.5:1) | " + count + " outlined";
    strip.style.cssText = "position:fixed;left:0;right:0;bottom:0;z-index:99999;padding:10px 16px;font:600 15px/1.3 ui-monospace,monospace;background:#111;color:#fff;border-top:3px solid " + (onCard >= 4.5 && onPage >= 4.5 ? "#2e9e5b" : "#d11a2a");
    document.body.appendChild(strip);
    return count;
  }, theme);
  expect(n).toBeGreaterThan(0);
}

async function scrollThrough(page: Page) {
  const h = await page.evaluate(() => document.documentElement.scrollHeight);
  for (let y = 0; y < h; y += 380) {
    await page.evaluate((v) => window.scrollTo(0, v), y);
    await beat(page, 700);
  }
  await page.evaluate(() => window.scrollTo(0, 0));
  await beat(page, 600);
}

test("#1235 walkthrough: --text-3 text on a backup set's page, light then dark", async ({ page }) => {
  await open(page, "/sets/production/postgres-primary");
  await expect(page.locator("h1").first()).toBeVisible({ timeout: 30_000 });
  await beat(page, 1500);
  for (const theme of ["light", "dark"] as const) {
    // After the app has booted: a theme set earlier is overwritten by its start-up.
    await setTheme(page, theme);
    await annotate(page, theme);
    await beat(page, 1500);
    await scrollThrough(page);
  }
});
