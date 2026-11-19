/**
 * Issue #1505: a backup set's activity on the Dashboard has no way to open
 * that set's terminal output.
 *
 * What I want: a "Show in Terminal" button beside "Hide log" on each set's
 * activity. Pressing it does what I would do by hand: it is the same as
 * pressing "Show terminal" on the bottom status bar and then pressing the
 * chip for that set, so the terminal opens with that set's output selected.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const strips = (p: Page) => p.getByRole("region", { name: /^Activity for / });
const terminal = (p: Page) => p.getByRole("region", { name: "Terminal" });
const chipGroup = (p: Page) => terminal(p).getByRole("group", { name: "Filter the terminal" });
const pressedChips = (p: Page) => chipGroup(p).locator('button[aria-pressed="true"]');
const showIn = (p: Page, i: number) => strips(p).nth(i).getByRole("button", { name: "Show in Terminal" });

/** The Dashboard, with the terminal collapsed or open. The dock starts open
 *  and the setting is stored once it is hidden, so a person who wants this
 *  shortcut has usually collapsed it. */
async function openDashboard(p: Page, dock: "collapsed" | "open" = "collapsed") {
  if (dock === "collapsed") await p.addInitScript(() => window.localStorage.setItem("retnd.dock.open", "0"));
  await open(p, "/");
  // The strips arrive with the first read of the feed, so wait for the set
  // list rather than counting what is there at load.
  await expect(strips(p).nth(1)).toBeVisible();
}

/** The set id the button was drawn for. The strips are named by display name
 *  and the terminal's chips by id, so the button carries the id. */
async function setIdOf(p: Page, i: number) {
  return (await showIn(p, i).getAttribute("data-set-id"))!;
}

test.describe("#1505 Show in Terminal on a set's Dashboard activity", () => {
  test("#1505 every set's activity has a Show in Terminal button next to Hide log", async ({ page }) => {
    await openDashboard(page);
    const n = await strips(page).count();
    expect(n).toBeGreaterThan(1);
    for (let i = 0; i < n; i++) {
      const strip = strips(page).nth(i);
      await expect(strip.getByRole("button", { name: "Hide log" })).toBeVisible();
      await expect(strip.getByRole("button", { name: "Show in Terminal" })).toBeVisible();
    }
  });

  test("#1505 the button sits right beside Hide log in the same toolbar", async ({ page }) => {
    await openDashboard(page);
    const strip = strips(page).first();
    const hide = await strip.getByRole("button", { name: "Hide log" }).boundingBox();
    const show = await strip.getByRole("button", { name: "Show in Terminal" }).boundingBox();
    expect(hide).not.toBeNull();
    expect(show).not.toBeNull();
    // Same row, and nothing else between them: the gap is smaller than one button.
    expect(Math.abs(show!.y - hide!.y)).toBeLessThan(hide!.height);
    expect(Math.abs(show!.x - (hide!.x + hide!.width))).toBeLessThan(hide!.width);
  });

  test("#1505 pressing it opens the terminal, as Show terminal does", async ({ page }) => {
    await openDashboard(page);
    await expect(terminal(page).getByRole("button", { name: "Show terminal" })).toHaveAttribute("aria-expanded", "false");
    await showIn(page, 0).click();
    await expect(terminal(page).getByRole("button", { name: "Hide terminal" })).toHaveAttribute("aria-expanded", "true");
  });

  test("#1505 and it selects exactly the chip of the set whose button was pressed", async ({ page }) => {
    await openDashboard(page);
    const id = await setIdOf(page, 0);
    await showIn(page, 0).click();
    await expect(pressedChips(page)).toHaveCount(1);
    await expect(pressedChips(page)).toHaveText(id);
    await expect(chipGroup(page).getByRole("button", { name: "Everything" })).toHaveAttribute("aria-pressed", "false");
  });

  test("#1505 a different set's button selects that set's chip, not the first one's", async ({ page }) => {
    await openDashboard(page);
    const first = await setIdOf(page, 0);
    const second = await setIdOf(page, 1);
    expect(second).not.toBe(first);
    await showIn(page, 0).click();
    await expect(pressedChips(page)).toHaveText(first);
    await showIn(page, 1).click();
    await expect(pressedChips(page)).toHaveCount(1);
    await expect(pressedChips(page)).toHaveText(second);
  });

  test("#1505 it works when the terminal is already open, and moves the selection", async ({ page }) => {
    await openDashboard(page, "open");
    await expect(terminal(page).getByRole("button", { name: "Hide terminal" })).toHaveAttribute("aria-expanded", "true");
    const id = await setIdOf(page, 1);
    await showIn(page, 1).click();
    await expect(pressedChips(page)).toHaveText(id);
    await expect(terminal(page).getByRole("button", { name: "Hide terminal" })).toHaveAttribute("aria-expanded", "true");
  });

  test("#1505 it drops a level filter, which would otherwise hide the set's lines", async ({ page }) => {
    await openDashboard(page, "open");
    const warnings = terminal(page).getByRole("button", { name: /^Show \d+ warnings?$/ });
    await warnings.click();
    await expect(warnings).toHaveAttribute("aria-pressed", "true");
    await showIn(page, 0).click();
    await expect(warnings).toHaveAttribute("aria-pressed", "false");
  });

  test("#1505 the selection is remembered like a chip pressed by hand", async ({ page }) => {
    await openDashboard(page);
    const id = await setIdOf(page, 0);
    await showIn(page, 0).click();
    await expect(pressedChips(page)).toHaveText(id);
    expect(await page.evaluate(() => window.localStorage.getItem("retnd.dock.filter"))).toBe(id);
  });

  test("#1505 it says what it did, for a screen reader", async ({ page }) => {
    await openDashboard(page);
    const id = await setIdOf(page, 0);
    await showIn(page, 0).click();
    await expect(terminal(page).getByRole("status").filter({ hasText: "Terminal opened, showing " + id })).toHaveCount(1);
  });
});
