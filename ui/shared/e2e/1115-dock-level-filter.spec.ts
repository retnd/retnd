/**
 * Issue #1115: the counts in the activity dock are toggles that filter the
 * terminal to one level.
 *
 * The default fixture holds exactly one warning (the unattributed
 * "record-failure" line) and one error (the cycle that finished with an
 * error). "dock-no-warnings" is the same feed with the warning taken out, and
 * "dock-live-line" lets a spec make a warning and a plain note arrive while
 * a filter is on.
 */
import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { open, setTheme } from "./helpers";

const WARNING_TEXT = "could not record FAILED";
const ERROR_TEXT = "cycle finished with an error";
const NOTE_TEXT = "backup set updated";

const warnBtn = (p: Page) => p.getByRole("button", { name: /^Show \d+ warnings?$/ });
const errBtn = (p: Page) => p.getByRole("button", { name: /^Show \d+ errors?$/ });
const countLine = (p: Page) => p.getByText(/^\d+ (of \d+ )?lines?$/);
const log = (p: Page) => p.getByRole("log");

async function opened(p: Page, scenario = "default") {
  await open(p, "/", scenario);
  await expect(log(p)).toContainText(NOTE_TEXT);
}

test.describe("#1115 dock level filter", () => {
  test("#1115 the warning and error counts are buttons named for what they show", async ({ page }) => {
    await opened(page);
    await expect(page.getByRole("button", { name: "Show 1 warning" })).toHaveAttribute("aria-pressed", "false");
    await expect(page.getByRole("button", { name: "Show 1 error" })).toHaveAttribute("aria-pressed", "false");
  });

  test("#1115 pressing Warnings keeps only warning lines, says N of M, and pressing again clears it", async ({ page }) => {
    await opened(page);
    const all = (await countLine(page).textContent())!.match(/^(\d+) lines?$/)![1];
    await warnBtn(page).click();
    await expect(warnBtn(page)).toHaveAttribute("aria-pressed", "true");
    await expect(log(page)).toContainText(WARNING_TEXT);
    await expect(log(page)).not.toContainText(ERROR_TEXT);
    await expect(log(page)).not.toContainText(NOTE_TEXT);
    await expect(countLine(page)).toHaveText("1 of " + all + " lines");
    await warnBtn(page).click();
    await expect(warnBtn(page)).toHaveAttribute("aria-pressed", "false");
    await expect(log(page)).toContainText(NOTE_TEXT);
    await expect(countLine(page)).toHaveText(all + " lines");
  });

  test("#1115 pressing Errors keeps only error lines", async ({ page }) => {
    await opened(page);
    await errBtn(page).click();
    await expect(errBtn(page)).toHaveAttribute("aria-pressed", "true");
    await expect(log(page)).toContainText(ERROR_TEXT);
    await expect(log(page)).not.toContainText(WARNING_TEXT);
    await expect(log(page)).not.toContainText(NOTE_TEXT);
  });

  test("#1115 one level at a time: pressing the other count switches rather than adds", async ({ page }) => {
    await opened(page);
    await warnBtn(page).click();
    await errBtn(page).click();
    await expect(errBtn(page)).toHaveAttribute("aria-pressed", "true");
    await expect(warnBtn(page)).toHaveAttribute("aria-pressed", "false");
    await expect(log(page)).toContainText(ERROR_TEXT);
    await expect(log(page)).not.toContainText(WARNING_TEXT);
  });

  test("#1115 a level filter composes with the existing source chips", async ({ page }) => {
    await opened(page);
    await warnBtn(page).click();
    await page.getByRole("button", { name: "This browser" }).click();
    await expect(log(page)).not.toContainText(WARNING_TEXT);
    await expect(warnBtn(page)).toHaveAttribute("aria-pressed", "true");
  });

  test("#1115 a count of zero is shown but cannot be pressed", async ({ page }) => {
    await opened(page, "dock-no-warnings");
    const zero = page.getByRole("button", { name: "Show 0 warnings" });
    await expect(zero).toBeVisible();
    await expect(zero).toBeDisabled();
    await expect(errBtn(page)).toBeEnabled();
  });

  test("#1115 the buttons work from the keyboard", async ({ page }) => {
    await opened(page);
    await warnBtn(page).focus();
    await page.keyboard.press("Enter");
    await expect(warnBtn(page)).toHaveAttribute("aria-pressed", "true");
    await page.keyboard.press("Space");
    await expect(warnBtn(page)).toHaveAttribute("aria-pressed", "false");
  });

  test("#1115 toggling is announced politely, once, outside the log", async ({ page }) => {
    await opened(page);
    const status = page.getByRole("status").filter({ hasText: /lines/ });
    await warnBtn(page).click();
    await expect(status).toContainText("Showing 1 of");
    await expect(status).toContainText("warnings only");
  });

  test("#1115 the pressed state is not colour alone", async ({ page }) => {
    await opened(page);
    const style = (l: Locator) =>
      l.evaluate((e) => {
        const cs = getComputedStyle(e);
        return { w: cs.fontWeight, d: cs.textDecorationLine, s: cs.boxShadow, b: cs.borderTopWidth };
      });
    const before = await style(warnBtn(page));
    await warnBtn(page).click();
    await page.mouse.move(0, 0);
    const after = await style(warnBtn(page));
    expect(after).not.toEqual(before);
    expect(after.w !== before.w || after.d !== before.d || after.s !== before.s || after.b !== before.b).toBe(true);
  });

  test("#1115 a new live line meets the filter: a match appears, a non-match does not, counts keep counting everything", async ({ page }) => {
    await opened(page, "dock-live-line");
    await warnBtn(page).click();
    await expect(countLine(page)).toHaveText(/^1 of \d+ lines$/);
    await page.evaluate(() => sessionStorage.setItem("retnd.mock.dock.live", "1"));
    await expect(page.getByRole("button", { name: "Show 2 warnings" })).toBeVisible({ timeout: 10_000 });
    await expect(log(page)).toContainText("disk latency above threshold");
    await expect(log(page)).not.toContainText("retention pass complete");
    await expect(countLine(page)).toHaveText(/^2 of \d+ lines$/);
    await expect(warnBtn(page)).toHaveAttribute("aria-pressed", "true");
    // The note was counted even though it is hidden: clearing shows it.
    await warnBtn(page).click();
    await expect(log(page)).toContainText("retention pass complete");
  });
});

/** Contrast of a control's text against its own fill, and of its edge against
 *  the bar behind it, in the state it is in right now. */
async function pair(l: Locator) {
  return l.evaluate((e) => {
    const c = document.createElement("canvas").getContext("2d")!;
    const rgb = (s: string) => {
      c.clearRect(0, 0, 1, 1);
      c.fillStyle = "#000";
      c.fillStyle = s;
      c.fillRect(0, 0, 1, 1);
      const d = c.getImageData(0, 0, 1, 1).data;
      return [d[0], d[1], d[2], d[3] / 255];
    };
    const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
    const lum = (a: number[]) => 0.2126 * f(a[0]) + 0.7152 * f(a[1]) + 0.0722 * f(a[2]);
    const ratio = (a: number[], b: number[]) => { const [h, l2] = [lum(a), lum(b)].sort((x, y) => y - x); return Math.round(((h + 0.05) / (l2 + 0.05)) * 100) / 100; };
    const fill = (n: Element | null) => { for (; n; n = n.parentElement) { const x = rgb(getComputedStyle(n).backgroundColor); if (x[3] > 0.5) return x; } return [255, 255, 255, 1]; };
    const cs = getComputedStyle(e);
    const own = fill(e);
    const behind = fill(e.parentElement);
    return { text: ratio(rgb(cs.color), own), edge: ratio(rgb(cs.borderTopColor), behind), fg: cs.color, bg: cs.backgroundColor };
  });
}

for (const theme of ["light", "dark"] as const) {
  test(`#1115 contrast, ${theme} theme: rest, hover, pressed and disabled meet WCAG AA`, async ({ page }) => {
    await opened(page, "dock-no-warnings");
    await setTheme(page, theme);
    const err = errBtn(page);
    const zero = page.getByRole("button", { name: "Show 0 warnings" });
    const report: Record<string, { text: number; edge: number }> = {};
    await page.mouse.move(0, 0);
    report.rest = await pair(err);
    await err.hover();
    report.hover = await pair(err);
    await err.click();
    await page.mouse.move(0, 0);
    report.pressed = await pair(err);
    report.disabled = await pair(zero);
    for (const [state, m] of Object.entries(report)) {
      console.log(`CONTRAST #1115 ${theme} ${state} | text ${m.text}:1 (needs 4.5) | edge ${m.edge}:1 (needs 3)`);
    }
    for (const state of ["rest", "hover", "pressed", "disabled"]) {
      expect(report[state].text, `${theme} ${state} text`).toBeGreaterThanOrEqual(4.5);
      expect(report[state].edge, `${theme} ${state} edge`).toBeGreaterThanOrEqual(3);
    }
  });
}
