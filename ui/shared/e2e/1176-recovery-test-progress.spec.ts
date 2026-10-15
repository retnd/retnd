import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureBorders, measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1176: a running recovery test showed no progress bar movement and an
 * empty terminal of its own, when the set's terminal already carries the
 * output. Driven with `wire=1`, so realAssurance (the code a built app runs)
 * follows the mock's feed the way it follows a real server: stage lines only,
 * with no byte counts until the end.
 */
const SET = "/sets/production/postgres-primary";
const region = (page: Page) => page.getByRole("region", { name: "Assurance" });
const bar = (page: Page) => region(page).getByRole("progressbar", { name: "Recovery test progress" });

/** Records every distinct aria-valuenow the bar ever shows. Sampling would
 *  miss values that go by between two polls of a spec, so a recorder watches
 *  the DOM for them instead. */
async function recordBar(page: Page) {
  await page.evaluate(() => {
    const w = window as unknown as { __bar: string[] };
    w.__bar = [];
    const read = () => {
      const el = document.querySelector('[role="progressbar"][aria-label="Recovery test progress"]');
      const v = el?.getAttribute("aria-valuenow");
      if (v != null && w.__bar[w.__bar.length - 1] !== v) w.__bar.push(v);
    };
    new MutationObserver(read).observe(document.body, { subtree: true, attributes: true, childList: true });
    read();
  });
}
const recorded = (page: Page) => page.evaluate(() => (window as unknown as { __bar: string[] }).__bar.map(Number));

test.describe("#1176 recovery test progress and the set terminal", () => {
  test("the progress bar advances as the test moves through its stages, and ends full", async ({ page }) => {
    // the mock's test takes about 25 seconds; the default budget leaves no room
    test.setTimeout(90_000);
    await open(page, SET, "assurance-slow-test", { wire: true });
    await recordBar(page);
    await region(page).getByRole("button", { name: "Run recovery test" }).click();
    await expect(region(page).getByTestId("operation-progress")).toHaveAttribute("data-state", "complete", { timeout: 60_000 });
    const seen = await recorded(page);
    expect(seen.length, "the bar must report a value that changes: " + JSON.stringify(seen)).toBeGreaterThanOrEqual(4);
    expect(seen, "the value only moves forward").toEqual([...seen].sort((a, b) => a - b));
    expect(new Set(seen).size, "and never repeats a step").toBe(seen.length);
    expect(seen[seen.length - 1]).toBe(100);
    // a determinate bar is not the sliding indeterminate one
    await expect(bar(page)).not.toHaveAttribute("data-indeterminate", /.*/);
  });

  test("while it runs the bar is determinate and the panel says which step of how many", async ({ page }) => {
    await open(page, SET, "assurance-slow-test", { wire: true });
    await region(page).getByRole("button", { name: "Run recovery test" }).click();
    await expect(bar(page)).toHaveAttribute("aria-valuenow", /^\d+$/, { timeout: 15_000 });
    await expect(region(page).getByTestId("stage-count")).toHaveText(/^Step \d+ of \d+$/);
  });

  test("the dialog draws no terminal of its own and points at the set terminal, which the link focuses", async ({ page }) => {
    await open(page, SET, "assurance-slow-test", { wire: true });
    const section = region(page);
    await section.getByRole("button", { name: "Run recovery test" }).click();
    await expect(section.getByTestId("operation-progress")).toBeVisible();
    await expect(section.getByRole("list", { name: "Operation lines" }), "no embedded terminal").toHaveCount(0);
    await expect(section.getByTestId("operation-line")).toHaveCount(0);
    const link = section.getByRole("link", { name: "Output is in the set terminal" });
    await expect(link).toBeVisible();
    await link.click();
    const terminal = page.locator("#set-terminal");
    await expect(terminal).toBeFocused();
    await expect(terminal.getByRole("log")).toContainText("recovery test stage", { timeout: 20_000 });
  });

  for (const theme of ["light", "dark"] as const) {
    test("the progress, step count and pointer meet WCAG AA in the " + theme + " theme", async ({ page }) => {
      await open(page, SET, "assurance-slow-test", { wire: true });
      await setTheme(page, theme);
      await region(page).getByRole("button", { name: "Run recovery test" }).click();
      await expect(region(page).getByRole("link", { name: "Output is in the set terminal" })).toBeVisible();
      await expect(bar(page)).toHaveAttribute("aria-valuenow", /^\d+$/, { timeout: 15_000 });
      const sel = '[data-testid="operation-progress"]';
      expect(await measureContrast(page, "1176 " + theme, sel)).toEqual([]);
      expect(await measureBorders(page, "1176 " + theme, sel + ' [role="progressbar"]')).toEqual([]);
      // the filled part of the bar against its track, and the focus ring the
      // link draws around the set terminal against the card it sits in
      const pairs = await page.evaluate(() => {
        const c = document.createElement("canvas").getContext("2d")!;
        const rgb = (v: string) => { c.clearRect(0, 0, 1, 1); c.fillStyle = "#000"; c.fillStyle = v; c.fillRect(0, 0, 1, 1); return Array.from(c.getImageData(0, 0, 1, 1).data.slice(0, 3)); };
        const lum = (a: number[]) => { const f = (x: number) => { x /= 255; return x <= 0.03928 ? x / 12.92 : Math.pow((x + 0.055) / 1.055, 2.4); }; return 0.2126 * f(a[0]) + 0.7152 * f(a[1]) + 0.0722 * f(a[2]); };
        const ratio = (a: number[], b: number[]) => { const [h, l] = [lum(a), lum(b)].sort((x, y) => y - x); return Math.round(((h + 0.05) / (l + 0.05)) * 100) / 100; };
        const fill = document.querySelector('[role="progressbar"][aria-label="Recovery test progress"] > i') as HTMLElement;
        const track = fill.parentElement as HTMLElement;
        const term = document.getElementById("set-terminal") as HTMLElement;
        const page_ = (e: HTMLElement) => { for (let n: HTMLElement | null = e; n; n = n.parentElement) { const x = getComputedStyle(n).backgroundColor; if (!/rgba\(.*, 0\)|transparent/.test(x)) return x; } return "rgb(255,255,255)"; };
        const ring = getComputedStyle(document.documentElement).getPropertyValue("--accent").trim();
        return [
          { what: "bar fill on track", ratio: ratio(rgb(getComputedStyle(fill).backgroundColor), rgb(getComputedStyle(track).backgroundColor)) },
          { what: "focus ring on card", ratio: ratio(rgb(ring), rgb(page_(term))) }
        ];
      });
      for (const p of pairs) console.log("UI " + theme + " | " + p.ratio.toFixed(2) + ":1 (needs 3) | " + p.what);
      for (const p of pairs) expect(p.ratio, p.what).toBeGreaterThanOrEqual(3);
    });
  }
});
