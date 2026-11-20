/**
 * Issue #1592: leaving the bulk recovery dialog while its run is going.
 *
 * The dialog is modal and offers nothing but "Cancel remaining" until the run
 * ends, so the way out of it mid-run is the browser's own Back (or anything
 * else that takes the page away). The run used to keep going when that
 * happened, with no progress, no summary and no Cancel anywhere on screen. A
 * closed tab, a reload and Ctrl-C in `retnd recover` all cancel a run, since
 * the engine reads a closed connection as a cancel
 * (webhost/handlers_recoverymany.go), and now leaving the dialog does too: it
 * closes the run's connection on its way out.
 *
 * The run has nothing on screen once the dialog has gone, so these read the
 * mock's run probe (`window.__retndRecoveries`, mockRecoverMany.ts) for how
 * the run ended. Run with E2E_VIDEO=1 for the GIF.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const MANY = "backups-many";
const bar = (page: Page) => page.getByRole("region", { name: "Selected backups" });
const headerBox = (page: Page) => page.getByRole("checkbox", { name: "Select all matching backups" });
const dialog = (page: Page) => page.getByRole("dialog", { name: "Recovering backups" });
const beat = (page: Page, ms = 700) => (process.env.E2E_VIDEO ? page.waitForTimeout(ms) : Promise.resolve());

type Runs = { started: number; cancelled: number; finished: number };
const runs = (page: Page) =>
  page.evaluate(() => (window as unknown as { __retndRecoveries?: Runs }).__retndRecoveries ?? { started: 0, cancelled: 0, finished: 0 });

/** Arrives on Backups from the dashboard through the sidebar, so Back stays
 *  inside the app, and starts recovering every selectable backup (33 of
 *  them, 120ms each in this scenario, so the run lasts a few seconds). */
async function startRun(page: Page) {
  await open(page, "/", MANY);
  await page.getByRole("navigation").getByRole("link", { name: /^Backups/ }).click();
  await expect(page).toHaveURL(/\/backups/);
  await headerBox(page).check();
  await bar(page).getByRole("radio", { name: "Under the staging root on this retnd host" }).check();
  await bar(page).getByRole("textbox", { name: /Name under the staging root/ }).fill("drill-1592");
  await bar(page).getByRole("checkbox", { name: /I accept that the provider bills/ }).check();
  await beat(page);
  await bar(page).getByRole("button", { name: "Recover selected" }).click();
  await expect(dialog(page).getByTestId("run-progress")).toContainText(/\d+ of 33/);
  await expect.poll(() => runs(page)).toMatchObject({ started: 1 });
}

test.describe("#1592 leaving the recovery dialog mid-run", () => {
  test("#1592 going Back while a recovery runs closes its connection, so the run is cancelled", async ({ page }) => {
    await startRun(page);
    await beat(page, 1200);
    await page.goBack();
    await expect(dialog(page)).toHaveCount(0);
    await expect(page).not.toHaveURL(/\/backups/);
    await beat(page);

    // The mock stops after the file in flight, the way the engine does, and
    // reports the run as cancelled rather than finished.
    await expect.poll(() => runs(page), { timeout: 10_000 }).toMatchObject({ started: 1, cancelled: 1, finished: 0 });
    // And it stays that way: nothing went on recovering in the background.
    await page.waitForTimeout(5_000);
    expect(await runs(page)).toEqual({ started: 1, cancelled: 1, finished: 0 });
  });

  test("#1592 a run whose dialog stays open runs to the end and is not cancelled", async ({ page }) => {
    await startRun(page);
    await expect(dialog(page).getByTestId("run-summary")).toBeVisible({ timeout: 20_000 });
    await beat(page, 1200);
    expect(await runs(page)).toEqual({ started: 1, cancelled: 0, finished: 1 });
    // Close ends a run that has already ended, and cancels nothing.
    await dialog(page).getByRole("button", { name: "Close" }).click();
    await expect(dialog(page)).toHaveCount(0);
    await page.waitForTimeout(500);
    expect(await runs(page)).toEqual({ started: 1, cancelled: 0, finished: 1 });
  });
});
