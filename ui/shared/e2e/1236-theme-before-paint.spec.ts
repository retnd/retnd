/**
 * Issue #1236: the theme has to be on the page before the first paint, and
 * nothing the app does while it boots may take it away again.
 *
 * Since the async boot (#1216, #1223) the app's own start-up writes
 * `data-theme` from a React effect, after the first paint, so a stored dark
 * preference showed the light theme for the length of a module fetch, and a
 * theme a spec set earlier could be overwritten. #1232 made the specs wait for
 * the theme; this is the other half, the operator-visible flash.
 *
 * Both specs install a recorder through an init script, so it runs before any
 * app script does. It notes every change of `data-theme` and, on every
 * animation frame (a missing attribute counts: that is the light tokens),
 * the attribute and the painted background of the body. A dark page must never
 * be seen as anything but dark, and the same holds for light.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, preselectTheme } from "./helpers";

type Theme = "light" | "dark";

interface Frame { attr: string | null; bg: string | null; scheme: string | null }

/** Installs the recorder. Must be added before the preselect script. */
async function record(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as { __seen: Array<string | null>; __frames: Frame[] };
    w.__seen = [];
    w.__frames = [];
    const read = () => (document.documentElement ? document.documentElement.getAttribute("data-theme") : null);
    // Every change of the attribute, from whichever script makes it. The value
    // before the first change is not recorded: an init script runs ahead of
    // the page's own, so "nothing yet" is not a frame anyone saw.
    const note = () => {
      const v = read();
      if (w.__seen[w.__seen.length - 1] !== v || w.__seen.length === 0) w.__seen.push(v);
    };
    new MutationObserver(note).observe(document, { subtree: true, attributes: true, attributeFilter: ["data-theme"] });
    const tick = () => {
      w.__frames.push({
        attr: read(),
        bg: document.body ? getComputedStyle(document.body).backgroundColor : null,
        // What paints the canvas before any stylesheet has arrived: in the
        // development build the CSS comes in through a module, after the
        // first frames, so this is the only thing that can hold them dark.
        scheme: document.documentElement ? getComputedStyle(document.documentElement).colorScheme : null
      });
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });
}

/** What a stored preference looks like to the next load. */
async function storePreference(page: Page, theme: Theme) {
  await page.addInitScript((t) => window.localStorage.setItem("retnd.theme", t), theme);
}

/** What a spec that sets the attribute itself, before load, looks like. The
 *  root element may not exist yet when an init script runs, so it waits. */
async function presetAttribute(page: Page, theme: Theme) {
  await page.addInitScript((t) => {
    const set = () => {
      if (!document.documentElement) return false;
      document.documentElement.setAttribute("data-theme", t);
      return true;
    };
    if (set()) return;
    const mo = new MutationObserver(() => { if (set()) mo.disconnect(); });
    mo.observe(document, { childList: true });
  }, theme);
}

function luminance(css: string): number {
  const m = css.match(/[\d.]+/g)!.map(Number);
  const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
  return 0.2126 * f(m[0]) + 0.7152 * f(m[1]) + 0.0722 * f(m[2]);
}

async function verdict(page: Page, theme: Theme, label: string) {
  // Let the screen settle, then read what the recorder saw.
  await page.waitForSelector("#root *");
  await page.evaluate(() => new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(done, 300)))));
  const { seen, frames } = await page.evaluate(() => {
    const w = window as unknown as { __seen: Array<string | null>; __frames: Frame[] };
    return { seen: w.__seen, frames: w.__frames };
  });
  console.log("THEME-SEEN " + label + " | " + JSON.stringify(seen) + " | frames=" + frames.length);
  expect(seen, label + ": data-theme took a value other than " + theme + " (null is the light tokens)").toEqual([theme]);
  const wrong = frames.filter((f) => f.attr !== theme);
  expect(wrong.length, label + ": " + wrong.length + " of " + frames.length + " frames were not " + theme).toBe(0);
  const painted = frames.filter((f) => f.bg !== null && f.bg !== "rgba(0, 0, 0, 0)");
  const off = painted.filter((f) => (theme === "dark" ? luminance(f.bg!) > 0.3 : luminance(f.bg!) < 0.7));
  expect(off.length, label + ": " + off.length + " painted frames had the wrong background").toBe(0);
  const unthemed = frames.filter((f) => f.scheme !== theme);
  expect(unthemed.length, label + ": " + unthemed.length + " of " + frames.length + " frames had color-scheme " + [...new Set(unthemed.map((f) => f.scheme))].join("/") + " on the root, so the canvas was not " + theme).toBe(0);
  await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
}

const SCREENS = ["/", "/backups", "/settings"];

test.describe("#1236 the theme is on the page from the first frame", () => {
  for (const theme of ["dark", "light"] as const) {
    for (const path of SCREENS) {
      test("stored " + theme + " preference, " + path, async ({ page }) => {
        await record(page);
        await storePreference(page, theme);
        await open(page, path);
        await verdict(page, theme, "stored " + theme + " " + path);
      });

      test("attribute set before load, " + theme + ", " + path, async ({ page }) => {
        await record(page);
        await presetAttribute(page, theme);
        await open(page, path);
        await verdict(page, theme, "preset " + theme + " " + path);
      });
    }
  }

  for (const theme of ["dark", "light"] as const) {
    test("the shared preselectTheme helper, " + theme, async ({ page }) => {
      await record(page);
      await preselectTheme(page, theme);
      await open(page, "/backups");
      await verdict(page, theme, "helper " + theme);
    });
  }
});
