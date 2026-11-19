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

/** The Dashboard with the terminal collapsed, which is the state a shortcut
 *  is for: the dock starts open, and the setting is stored once it is hidden. */
async function openCollapsed(p: Page) {
  await p.addInitScript(() => window.localStorage.setItem("retnd.dock.open", "0"));
  await open(p, "/");
  // The strips arrive with the first read of the feed, so wait for the set
  // list rather than counting what is there at load.
  await expect(strips(p).nth(1)).toBeVisible();
}

test.describe("#1505 Show in Terminal on a set's Dashboard activity", () => {
  test("#1505 every set's activity has a Show in Terminal button next to Hide log", async ({ page }) => {
    await openCollapsed(page);
    const n = await strips(page).count();
    expect(n).toBeGreaterThan(1);
    for (let i = 0; i < n; i++) {
      const strip = strips(page).nth(i);
      await expect(strip.getByRole("button", { name: "Hide log" })).toBeVisible();
      await expect(strip.getByRole("button", { name: "Show in Terminal" })).toBeVisible();
    }
  });

  test("#1505 the button sits right beside Hide log in the same toolbar", async ({ page }) => {
    await openCollapsed(page);
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
    await openCollapsed(page);
    await expect(terminal(page).getByRole("button", { name: "Show terminal" })).toHaveAttribute("aria-expanded", "false");
    await strips(page).first().getByRole("button", { name: "Show in Terminal" }).click();
    await expect(terminal(page).getByRole("button", { name: "Hide terminal" })).toHaveAttribute("aria-expanded", "true");
  });

  test("#1505 and it selects a set chip, so only that set's output shows", async ({ page }) => {
    await openCollapsed(page);
    await strips(page).first().getByRole("button", { name: "Show in Terminal" }).click();
    const pressed = chipGroup(page).locator('button[aria-pressed="true"]');
    await expect(pressed).toHaveCount(1);
    // The chips are named by set id; the fixed ones are not a set.
    await expect(pressed).not.toHaveText(/^(Everything|Engine|This browser|Commands only)$/);
    await expect(chipGroup(page).getByRole("button", { name: "Everything" })).toHaveAttribute("aria-pressed", "false");
  });

  test("#1505 a different set's button selects that set's chip, not the first one's", async ({ page }) => {
    await openCollapsed(page);
    const pressed = chipGroup(page).locator('button[aria-pressed="true"]');
    await strips(page).first().getByRole("button", { name: "Show in Terminal" }).click();
    await expect(pressed).toHaveCount(1);
    const first = await pressed.textContent();
    await strips(page).nth(1).getByRole("button", { name: "Show in Terminal" }).click();
    await expect(pressed).toHaveCount(1);
    const second = await pressed.textContent();
    expect(second).not.toBe(first);
  });

  test("#1505 pressing it again from another set moves the selection rather than adding to it", async ({ page }) => {
    await openCollapsed(page);
    await strips(page).first().getByRole("button", { name: "Show in Terminal" }).click();
    await strips(page).nth(1).getByRole("button", { name: "Show in Terminal" }).click();
    await expect(chipGroup(page).locator('button[aria-pressed="true"]')).toHaveCount(1);
  });
});
