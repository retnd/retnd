import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, spaGo } from "./helpers";

/**
 * Issue #1177: every terminal kept its own copy of the lines, so each polled
 * on its own clock, they could disagree for a tick, and one opened late
 * started empty. The fixtures count the readings a page asks for
 * (`__retndLive.reads`) and can hold the feed shut (`__retndLive.gate`), which
 * is how these tell "drew from what the page holds" from "asked again".
 */
const SET = "/sets/production/postgres-primary";
const dock = (page: Page) => page.getByLabel("Terminal", { exact: true }).getByRole("log");
/** The set's own terminal: the Activity card on its page. */
const setTerminal = (page: Page) =>
  page.locator("section.card").filter({ has: page.getByRole("heading", { name: "Activity", exact: true }) }).getByRole("log");
const reads = (page: Page) => page.evaluate(() => (window as unknown as { __retndLive?: { reads: number } }).__retndLive?.reads ?? 0);

test.describe("#1177 one terminal state, every terminal subscribed", () => {
  test("two terminals on one page cost one reading per poll, not one each", async ({ page }) => {
    await open(page, SET);
    await expect(setTerminal(page)).toContainText("discovery", { timeout: 10_000 });
    await expect(dock(page)).toContainText("discovery");
    const before = await reads(page);
    await page.waitForTimeout(4000);
    const asked = (await reads(page)) - before;
    // the service asks for one reading a second; four seconds is four readings
    // for the page, plus one for the edge of the window
    expect(asked, "readings in 4s with the dock and the set terminal both mounted").toBeLessThanOrEqual(5);
    expect(asked).toBeGreaterThanOrEqual(3);
  });

  test("a terminal opened late shows what the others already hold, without a reading of its own", async ({ page }) => {
    await open(page, "/");
    await expect(dock(page)).toContainText("discovery", { timeout: 10_000 });
    // hold the feed shut: anything drawn from here on came from held state
    await page.evaluate(() => { (window as unknown as { __retndLive: { gate: Promise<void> | null } }).__retndLive.gate = new Promise(() => {}); });
    await spaGo(page, SET);
    await expect(setTerminal(page)).toContainText("discovery", { timeout: 2_000 });
    await expect(setTerminal(page)).toContainText("committed postgres-2026-08-28.dump.zst");
  });

  test("the set terminal and the global one show the same lines for the set", async ({ page }) => {
    await open(page, SET);
    const lines = [
      "discovery complete: 41 artifacts, 26 pending",
      "verified postgres-2026-08-28.dump.zst",
      "committed postgres-2026-08-28.dump.zst",
      "transfer started: postgres-2026-08-29.dump.zst"
    ];
    for (const l of lines) {
      await expect(setTerminal(page)).toContainText(l, { timeout: 10_000 });
      await expect(dock(page)).toContainText(l);
    }
    const countIn = (text: string, l: string) => text.split(l).length - 1;
    const mine = (await setTerminal(page).textContent()) ?? "";
    const global = (await dock(page).textContent()) ?? "";
    for (const l of lines) expect(countIn(global, l), l).toBe(countIn(mine, l));
  });
});
