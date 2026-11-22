/**
 * Issue #1652: on the Retained backups table the row checkbox sits about 16px
 * in from the card's left edge, but the Actions button runs right up to the
 * card's right edge. The table is min-width 1100px inside a .table-scroll
 * wrapper, so in a normal window it scrolls sideways and its last column ends
 * flush with the card.
 *
 * These are red until the trailing edge gets the same gutter as the leading
 * one. Each spec compares the left gap (card edge to the checkbox) with the
 * right gap (Actions button to the card edge), with a 1px tolerance, in both
 * themes, for every body row, and for the header row.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, setTheme } from "./helpers";

const TOL = 1;
// The default fixtures list a Medium column, which makes the table wider than
// the card at any window size. This scenario lists 30 backups with local copies
// only (no Medium column), like the list in the report. The card stops growing
// at about 1184px, so a 1900px window shows the same card as a wider one.
const SCENARIO = "backups-many-artifact-sets";
const WINDOWS = [
  { name: "narrow", width: 1280, height: 900 },
  { name: "wide", width: 1900, height: 1000 }
];

async function backups(page: Page, width: number, height: number, theme: "light" | "dark") {
  await page.setViewportSize({ width, height });
  await open(page, "/backups", SCENARIO);
  await expect(page.locator(".card .table tbody tr").nth(1)).toBeVisible({ timeout: 30_000 });
  await setTheme(page, theme);
}

/** Scrolls the table's wrapper to its far left (the checkbox column in view)
 *  or its far right (the trailing Actions column in view). */
async function scrollTo(page: Page, end: "left" | "right") {
  await page.locator(".card .table-scroll").first().evaluate((e, to) => { e.scrollLeft = to === "left" ? 0 : e.scrollWidth; }, end);
  await page.waitForTimeout(150);
}

interface Gaps { rows: number; left: number[]; right: number[]; clipped: string[]; scrolls: boolean; headLeft: number; headRight: number }

/** Measures every gap in px, in the browser. The header's right gap is taken
 *  to the content edge of its trailing cell (its box less its padding), as that
 *  cell holds only visually hidden text. */
async function measure(page: Page): Promise<Gaps> {
  await scrollTo(page, "left");
  const lefts = await page.evaluate(() => {
    const card = document.querySelector(".card .table")!.closest(".card")!.getBoundingClientRect();
    const r = (n: number) => Math.round(n * 10) / 10;
    return {
      left: [...document.querySelectorAll(".card .table tbody tr")].map((tr) => r(tr.querySelector<HTMLElement>("td.selcell input[type=checkbox]")!.getBoundingClientRect().left - card.left)),
      headLeft: r(document.querySelector<HTMLElement>(".card .table thead th.selcell input[type=checkbox]")!.getBoundingClientRect().left - card.left)
    };
  });
  await scrollTo(page, "right");
  const rest = await page.evaluate(() => {
    const card = document.querySelector(".card .table")!.closest(".card")!.getBoundingClientRect();
    const scroller = document.querySelector(".card .table-scroll")!;
    const sc = scroller.getBoundingClientRect();
    const trs = [...document.querySelectorAll(".card .table tbody tr")];
    const right: number[] = [];
    const clipped: string[] = [];
    trs.forEach((tr, i) => {
      const btn = tr.querySelector<HTMLElement>("td.actcell button")!.getBoundingClientRect();
      right.push(Math.round((card.right - btn.right) * 10) / 10);
      if (btn.right > card.right + 0.5 || btn.right > sc.right + 0.5 || btn.left < sc.left - 0.5) clipped.push("row " + i + ": button " + btn.left.toFixed(1) + ".." + btn.right.toFixed(1) + " in card " + card.left.toFixed(1) + ".." + card.right.toFixed(1));
    });
    const hAct = document.querySelector<HTMLElement>(".card .table thead th.actcell")!;
    const hr = hAct.getBoundingClientRect();
    const edge = hr.right - parseFloat(getComputedStyle(hAct).paddingRight);
    return {
      rows: trs.length, right, clipped,
      scrolls: scroller.scrollWidth > scroller.clientWidth + 1,
      headRight: Math.round((card.right - edge) * 10) / 10
    };
  });
  return { ...lefts, ...rest };
}

const note = (g: Gaps) => "left " + g.left.join(",") + " px, right " + g.right.join(",") + " px";

for (const w of WINDOWS) {
  for (const theme of ["light", "dark"] as const) {
    const tag = w.name + " " + w.width + "px, " + theme;
    test.describe("#1652 Backups table gutters, " + tag, () => {
      test("the window is what the spec says it is (#1652)", async ({ page }) => {
        await backups(page, w.width, w.height, theme);
        const g = await measure(page);
        expect(g.rows, "body rows drawn").toBeGreaterThan(1);
        // Narrow: the 1100px table scrolls inside the card. Wide: the card is
        // about as wide as the table, so it may or may not; the overflow is
        // written on the test so a run shows it.
        test.info().annotations.push({ type: "overflow", description: g.scrolls ? "table scrolls sideways" : "table fits the card" });
        if (w.name === "narrow") expect(g.scrolls, "table scrolls sideways").toBe(true);
      });

      test("the Actions button is as far from the card's right edge as the checkbox is from its left (#1652)", async ({ page }) => {
        await backups(page, w.width, w.height, theme);
        const g = await measure(page);
        expect(Math.abs(g.right[0] - g.left[0]), "first row: " + note(g)).toBeLessThanOrEqual(TOL);
      });

      test("every body row has matching left and right gaps (#1652)", async ({ page }) => {
        await backups(page, w.width, w.height, theme);
        const g = await measure(page);
        const off = g.left.map((l, i) => ({ row: i, left: l, right: g.right[i] })).filter((r) => Math.abs(r.right - r.left) > TOL);
        expect(off, "rows whose gaps differ by more than " + TOL + "px").toEqual([]);
      });

      test("the header row's select-all gap matches the gap at the trailing header cell (#1652)", async ({ page }) => {
        await backups(page, w.width, w.height, theme);
        const g = await measure(page);
        expect(Math.abs(g.headRight - g.headLeft), "header: left " + g.headLeft + " px, right " + g.headRight + " px").toBeLessThanOrEqual(TOL);
      });

      test("the Actions button sits fully inside the card (#1652)", async ({ page }) => {
        await backups(page, w.width, w.height, theme);
        const g = await measure(page);
        expect(g.clipped, "buttons outside the card or its scroll area").toEqual([]);
        // Inside is not enough: it must not touch the edge.
        expect(Math.min(...g.right), "smallest right gap, " + note(g)).toBeGreaterThan(TOL);
      });
    });
  }
}
