/**
 * Issue #1509: the "Terminal log for <set>" caption on a Dashboard strip floats
 * over the log lines (it is sticky inside the scrolling log) and is drawn in a
 * light blue that clashes with the dark terminal.
 *
 * What I want: it is a header directly above the log box. It does not scroll
 * with the log, never covers a line, and is drawn in the terminal's own colours
 * so it reads as the top of the terminal, in both themes.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, setTheme } from "./helpers";

const strips = (p: Page) => p.getByRole("region", { name: /^Activity for / });
const logOf = (p: Page, i = 0) => strips(p).nth(i).getByRole("log");
const captionOf = (p: Page, i = 0) => strips(p).nth(i).getByText(/^Terminal log for /).first();

async function opened(p: Page) {
  await open(p, "/");
  await expect(strips(p).nth(1)).toBeVisible();
  await expect(captionOf(p)).toBeVisible();
}

test.describe("#1509 the log caption is a header above the terminal", () => {
  test("#1509 the caption is not inside the scrolling log", async ({ page }) => {
    await opened(page);
    await expect(logOf(page).getByText(/^Terminal log for /)).toHaveCount(0);
  });

  test("#1509 the caption does not float: it is not sticky, fixed or absolute", async ({ page }) => {
    await opened(page);
    const position = await captionOf(page).evaluate((el) => {
      // The caption element and every ancestor up to the strip.
      const out: string[] = [];
      for (let n: Element | null = el; n && !n.matches('section[aria-label^="Activity for "]'); n = n.parentElement) out.push(getComputedStyle(n).position);
      return out;
    });
    for (const p of position) expect(["static", "relative"]).toContain(p);
  });

  test("#1509 it sits wholly above the log box, so it can never cover a line", async ({ page }) => {
    await opened(page);
    const cap = (await captionOf(page).boundingBox())!;
    const log = (await logOf(page).boundingBox())!;
    expect(cap.y + cap.height).toBeLessThanOrEqual(log.y + 1);
  });

  test("#1509 scrolling the log does not move the caption, and nothing is drawn over it", async ({ page }) => {
    await opened(page);
    // The first strip can sit below the fold; the check is about what is drawn where it is on screen.
    await captionOf(page).scrollIntoViewIfNeeded();
    const before = (await captionOf(page).boundingBox())!;
    await logOf(page).evaluate((el) => { el.scrollTop = el.scrollHeight; });
    const after = (await captionOf(page).boundingBox())!;
    expect(after.y).toBe(before.y);
    // Nothing is drawn over the caption: the topmost element at its centre is the caption itself.
    const covered = await captionOf(page).evaluate((el) => {
      const r = el.getBoundingClientRect();
      const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !(hit && (hit === el || el.contains(hit) || hit.contains(el)));
    });
    expect(covered).toBe(false);
  });

  for (const theme of ["light", "dark"] as const) {
    test("#1509 in the " + theme + " theme it is drawn in the terminal's own colours and reads at 4.5:1", async ({ page }) => {
      await opened(page);
      await setTheme(page, theme);
      const r = await page.evaluate(() => {
        const strip = document.querySelector('section[aria-label^="Activity for "]')!;
        const log = strip.querySelector('[role="log"]')!;
        const cap = [...strip.querySelectorAll("*")].find((e) => /^Terminal log for /.test((e.textContent ?? "").trim()) && e.children.length === 0)!;
        // The caption's effective background: its own, else the nearest ancestor's.
        const bgOf = (n: Element | null): string => {
          for (; n; n = n.parentElement) { const c = getComputedStyle(n).backgroundColor; if (c !== "rgba(0, 0, 0, 0)" && c !== "transparent") return c; }
          return "rgb(255, 255, 255)";
        };
        const lum = (c: string) => {
          const cv = document.createElement("canvas").getContext("2d")!;
          cv.fillStyle = "#000"; cv.fillStyle = c; cv.fillRect(0, 0, 1, 1);
          const d = cv.getImageData(0, 0, 1, 1).data;
          const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
          return 0.2126 * f(d[0]) + 0.7152 * f(d[1]) + 0.0722 * f(d[2]);
        };
        const fg = getComputedStyle(cap).color, bg = bgOf(cap);
        const [hi, lo] = [lum(fg), lum(bg)].sort((a, b) => b - a);
        return { bg, logBg: getComputedStyle(log).backgroundColor, ratio: (hi + 0.05) / (lo + 0.05) };
      });
      console.log("CONTRAST #1509 caption, " + theme + " | " + r.ratio.toFixed(2) + ":1 (needs 4.5) on " + r.bg);
      expect(r.bg).toBe(r.logBg);
      expect(r.ratio).toBeGreaterThanOrEqual(4.5);
    });
  }
});
