/**
 * Slow, watchable passes over the #1176 and #1177 behaviour, for the GIFs in
 * the PR. Each one ends on a DOM assertion about the state it shows, so a GIF
 * that looks right cannot be a page that is not. Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, spaGo } from "./helpers";

const SET = "/sets/production/postgres-primary";
const region = (p: Page) => p.getByRole("region", { name: "Assurance" });
const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);
const centre = (p: Page, sel: string) =>
  p.evaluate((s) => document.querySelector(s)?.scrollIntoView({ block: "center" }), sel);

test("#1176 walkthrough: the bar moves through the test's stages and ends full", async ({ page }) => {
  test.setTimeout(90_000);
  await open(page, SET, "assurance-slow-test", { wire: true });
  const run = region(page).getByRole("button", { name: "Run recovery test" });
  await run.scrollIntoViewIfNeeded();
  await beat(page);
  await run.click();
  await expect(region(page).getByTestId("operation-progress")).toBeVisible();
  await centre(page, '[data-testid="operation-progress"]');
  await expect(region(page).getByTestId("operation-progress")).toHaveAttribute("data-state", "complete", { timeout: 60_000 });
  await expect(region(page).getByRole("progressbar", { name: "Recovery test progress" })).toHaveAttribute("aria-valuenow", "100");
  await beat(page, 2000);
});

test("#1176 walkthrough: no terminal in the card, and the link focuses the set terminal", async ({ page }) => {
  await open(page, SET, "assurance-slow-test", { wire: true });
  const run = region(page).getByRole("button", { name: "Run recovery test" });
  await run.scrollIntoViewIfNeeded();
  await run.click();
  await expect(region(page).getByTestId("operation-progress")).toBeVisible();
  await centre(page, '[data-testid="operation-progress"]');
  await expect(region(page).getByTestId("stage-count")).toHaveText(/^Step [2-9] of 6$/, { timeout: 20_000 });
  await beat(page, 1500);
  await region(page).getByRole("link", { name: "Output is in the set terminal" }).click();
  const terminal = page.locator("#set-terminal");
  await expect(terminal).toBeFocused();
  await expect(terminal.getByRole("log")).toContainText("recovery test stage", { timeout: 20_000 });
  await beat(page, 2500);
});

test("#1177 walkthrough: a terminal opened late shows what the dock already holds", async ({ page }) => {
  await open(page, "/");
  const dock = page.getByLabel("Terminal", { exact: true }).getByRole("log");
  await expect(dock).toContainText("discovery", { timeout: 10_000 });
  await beat(page, 1500);
  // hold the feed shut: anything drawn from here on came from held state
  await page.evaluate(() => { (window as unknown as { __retndLiveGate: Promise<void> }).__retndLiveGate = new Promise(() => {}); });
  await spaGo(page, SET);
  const mine = page.locator("#set-terminal");
  await expect(mine.getByRole("log")).toContainText("committed postgres-2026-08-28.dump.zst", { timeout: 2_000 });
  await beat(page, 600);
  await centre(page, "#set-terminal");
  await expect(mine).toBeInViewport();
  await beat(page, 2500);
});
