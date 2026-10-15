import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, measureContrast, measureBorders, setTheme } from "./helpers";

/**
 * Issue #1106: Recover this backup set, driven on the real set page.
 *
 * The mock host behind the development build classifies rows, refuses for cost
 * and for room, and "recovers" a file every 700ms (2.5s in the slow scenario),
 * so the running total, the single Cancel and the summary are all things these
 * specs watch happen. The digests it reports are made up; nothing here is
 * evidence about bytes, only about what the page says and does with the host's
 * answers.
 */
const SET = "/sets/production/postgres-primary";
const dialog = (page: Page) => page.getByRole("dialog", { name: "Recover this backup set" });
const go = (page: Page) => dialog(page).getByRole("button", { name: "Recover this backup set" });
async function openDialog(page: Page, scenario = "assurance-set-recover", path = SET) {
  await open(page, path, scenario);
  await page.getByRole("button", { name: "Recover this backup set", exact: true }).click();
  await expect(dialog(page)).toBeVisible();
}
async function toFolder(page: Page, name: string) {
  await dialog(page).getByRole("textbox", { name: /Recover the set to a folder/ }).fill(name);
}

test.describe("#1106 Recover this backup set: choosing", () => {
  test("the set page offers it, and the pre-flight lists what is and is not included before anything runs", async ({ page }) => {
    await openDialog(page);
    const dlg = dialog(page);
    const pre = dlg.getByTestId("preflight-summary");
    await expect(pre).toContainText("4 backups: 3 to recover");
    await expect(pre).toContainText("1 quarantined");
    await expect(dlg.getByTestId("preflight-space")).toContainText("free");
    const rows = dlg.getByRole("list", { name: "Backups in this recovery" });
    await expect(rows.locator('li[data-status="ready"]')).toHaveCount(3);
    await expect(rows.locator('li[data-status="quarantined"]')).toContainText("postgres-prod-20260825.dump.zst");
    await expect(rows.locator('li[data-status="quarantined"]')).toContainText("Quarantined");
    // nothing may run until a destination is named
    await expect(go(page)).toBeDisabled();
    await toFolder(page, "restore-1");
    await expect(go(page)).toBeEnabled();
  });

  test("shows the equivalent command, and the pre-flight form of it, as the choices change", async ({ page }) => {
    await openDialog(page);
    const dlg = dialog(page);
    await toFolder(page, "restore-1");
    await expect(dlg.getByText("retnd recover --set production/postgres-primary --to restore-1", { exact: true })).toBeVisible();
    await expect(dlg.getByText("retnd recover --set production/postgres-primary --to restore-1 --preflight", { exact: true })).toBeVisible();
    await dlg.getByRole("radio", { name: "The newest restore point only" }).check();
    await expect(dlg.getByText("retnd recover --set production/postgres-primary --newest-only --to restore-1", { exact: true })).toBeVisible();
    await dlg.getByRole("radio", { name: "Everything since" }).check();
    await dlg.getByLabel("Recover everything since this date").fill("2026-08-27");
    await expect(dlg.getByText("retnd recover --set production/postgres-primary --since 2026-08-27 --to restore-1", { exact: true })).toBeVisible();
    await dlg.getByRole("radio", { name: "Configured destination" }).check();
    await dlg.getByRole("combobox", { name: "Recover the set to destination" }).selectOption("offsite_s3");
    await expect(dlg.getByText("retnd recover --set production/postgres-primary --since 2026-08-27 --to medium:offsite_s3", { exact: true })).toBeVisible();
    await expect(dlg).not.toContainText("--accept-cost");
  });

  test("narrowing to the newest restore point or to a date redraws the pre-flight", async ({ page }) => {
    await openDialog(page);
    const dlg = dialog(page);
    await dlg.getByRole("radio", { name: "The newest restore point only" }).check();
    await expect(dlg.getByTestId("preflight-summary")).toContainText("1 to recover");
    await expect(dlg.getByRole("list", { name: "Backups in this recovery" }).locator('li[data-status="ready"]')).toHaveCount(1);
    await dlg.getByRole("radio", { name: "Everything since" }).check();
    await expect(dlg.getByTestId("preflight")).toContainText("Pick a date");
    await expect(go(page)).toBeDisabled();
    await dlg.getByLabel("Recover everything since this date").fill("2026-08-27");
    await expect(dlg.getByTestId("preflight-summary")).toContainText("2 to recover");
  });

  test("offers no browser download, and says why", async ({ page }) => {
    await openDialog(page);
    const dlg = dialog(page);
    await expect(dlg.getByRole("radio", { name: /browser/i })).toHaveCount(0);
    await expect(dlg.getByTestId("no-browser-download")).toContainText("no browser download for a set");
  });

  test("Escape closes it and focus goes back to the button that opened it", async ({ page }) => {
    await openDialog(page);
    await page.keyboard.press("Escape");
    await expect(dialog(page)).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Recover this backup set", exact: true })).toBeFocused();
  });
});

test.describe("#1106 Recover this backup set: cost and room", () => {
  test("a set with an archived copy shows the whole cost once, stays off until it is accepted, and then starts the thaw and recovers nothing", async ({ page }) => {
    await openDialog(page, "assurance-set-recover", "/sets/production/billing-mysql");
    const dlg = dialog(page);
    await toFolder(page, "billing-restore");
    const cost = dlg.getByRole("group", { name: "This recovery costs money" });
    await expect(cost).toContainText("1 of these 2 files cost money to read");
    await expect(cost).toContainText(/provider restore|restore it first/);
    // no invented price or time
    await expect(dlg).not.toContainText(/[$€£]\s?\d|\d+(\.\d+)?\s?(USD|EUR)|about \d+ hours/i);
    await expect(go(page)).toBeDisabled();
    await expect(dlg).not.toContainText("--acknowledge-cost");
    await page.keyboard.press("Enter");
    await expect(dlg).toHaveAttribute("data-phase", "choose");
    await cost.getByRole("checkbox", { name: /I accept the cost/ }).check();
    await expect(dlg.getByText("retnd recover --set production/billing-mysql --to billing-restore --acknowledge-cost", { exact: true })).toBeVisible();
    await expect(go(page)).toBeEnabled();
    await go(page).click();
    const result = dlg.getByTestId("result");
    await expect(result).toBeVisible({ timeout: 15_000 });
    await expect(result).toContainText("0 of 2 backups recovered");
    await expect(dlg.getByTestId("result-counts")).toContainText("Needs a thaw: 1");
    await expect(dlg.getByTestId("result-counts")).toContainText("Quarantined: 1");
    await expect(dlg.getByTestId("result-problems").locator('li[data-status="needs_thaw"]')).toContainText("billing-20260827.sql.gz");
  });

  test("a set that does not fit the staging disk is refused with the numbers before a byte is read", async ({ page }) => {
    await openDialog(page, "assurance-set-nospace");
    const dlg = dialog(page);
    await toFolder(page, "restore-1");
    const refusal = dlg.getByTestId("preflight-refusal");
    await expect(refusal).toBeVisible();
    await expect(refusal).toHaveAttribute("data-code", "RECOVERY_NO_SPACE");
    await expect(refusal).toContainText(/the files need \d+ bytes \(3 files\) and the staging disk has 40000000000 bytes free/);
    await expect(go(page)).toBeDisabled();
  });

  test("a set on the incremental engine is a snapshot restore, with the command, and cannot be run from here", async ({ page }) => {
    await openDialog(page, "default");
    const dlg = dialog(page);
    await expect(dlg.getByTestId("incremental-engine-set")).toContainText("snapshot restore");
    await expect(dlg.getByText("retnd snapshot restore production/postgres-primary --to <dir>", { exact: true })).toBeVisible();
    await expect(go(page)).toHaveCount(0);
  });
});

test.describe("#1106 Recover this backup set: running", () => {
  test("shows a running total and one line per file, then a summary that agrees with them", async ({ page }) => {
    await openDialog(page);
    const dlg = dialog(page);
    await toFolder(page, "restore-1");
    await go(page).click();
    const run = dlg.getByTestId("running");
    await expect(run).toBeVisible();
    await expect(dlg).toHaveAttribute("data-phase", "running");
    await expect(dlg.getByTestId("running-current")).toContainText("Reading and checking");
    await expect(dlg.getByTestId("running-total")).toContainText(/File \d of 3/);
    await expect(dlg.getByRole("button", { name: "Cancel the recovery" })).toBeVisible();
    // Escape does not abandon a run
    await page.keyboard.press("Escape");
    await expect(dlg).toBeVisible();
    await expect(dlg.getByTestId("running-lines").locator("li")).toHaveCount(1, { timeout: 5000 });

    const result = dlg.getByTestId("result");
    await expect(result).toBeVisible({ timeout: 15_000 });
    await expect(result).toHaveAttribute("data-cancelled", "false");
    await expect(result).toContainText("3 of 4 backups recovered");
    const counts = dlg.getByTestId("result-counts");
    await expect(counts).toContainText("Recovered: 3");
    await expect(counts).toContainText("Failed: 0");
    await expect(counts).toContainText("Quarantined: 1");
    await expect(dlg.getByTestId("result-manifest")).toContainText("recovery-manifest.json");
    // the quarantined backup is listed as not recovered and never as a file
    await expect(dlg.getByTestId("result-problems").locator("li")).toHaveCount(1);
    await expect(dlg.getByTestId("result-problems")).toContainText("postgres-prod-20260825.dump.zst");
    await expect(dlg).toContainText("matched to the digest recorded when it was ingested before it was placed");
  });

  test("a corrupt copy is reported failed and the others still arrive", async ({ page }) => {
    await openDialog(page, "assurance-set-corrupt");
    const dlg = dialog(page);
    await toFolder(page, "restore-1");
    await go(page).click();
    const result = dlg.getByTestId("result");
    await expect(result).toBeVisible({ timeout: 15_000 });
    const counts = dlg.getByTestId("result-counts");
    await expect(counts).toContainText("Recovered: 2");
    await expect(counts).toContainText("Failed: 1");
    const failed = dlg.getByTestId("result-problems").locator('li[data-status="failed"]');
    await expect(failed).toHaveCount(1);
    await expect(failed).toContainText("does not match the checksum recorded");
    await expect(result).toContainText("2 of 4 backups recovered");
  });

  test("one Cancel stops the run, keeps the finished files, and says nothing half-written was kept", async ({ page }) => {
    await openDialog(page, "assurance-set-slow");
    const dlg = dialog(page);
    await toFolder(page, "restore-1");
    await go(page).click();
    await expect(dlg.getByTestId("running-lines").locator("li")).toHaveCount(1, { timeout: 8000 });
    await dlg.getByRole("button", { name: "Cancel the recovery" }).click();
    await expect(dlg.getByTestId("running")).toContainText("Cancelling");
    const result = dlg.getByTestId("result");
    await expect(result).toBeVisible({ timeout: 8000 });
    await expect(result).toHaveAttribute("data-cancelled", "true");
    await expect(result).toContainText("Cancelled: 1 files are complete");
    await expect(dlg.getByTestId("result-cancelled-note")).toContainText("no half-written file was kept");
    const counts = dlg.getByTestId("result-counts");
    await expect(counts).toContainText("Recovered: 1");
    await expect(counts).toContainText("Skipped: 2");
    // a cancelled run does not claim a manifest name it never received
    await expect(dlg.getByTestId("result-manifest")).toHaveCount(0);
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`#1106 contrast, ${theme}: choosing, the cost decision, running and the result`, async ({ page }) => {
    await openDialog(page, "assurance-set-recover", "/sets/production/billing-mysql");
    await setTheme(page, theme);
    const dlg = dialog(page);
    await toFolder(page, "billing-restore");
    await expect(dlg.getByTestId("set-cost")).toBeVisible();
    expect(await measureContrast(page, `recover set dialog (cost), ${theme}`, '[role="dialog"][aria-labelledby="recover-set-title"]')).toEqual([]);
    expect(await measureBorders(page, `recover set controls (cost), ${theme}`, '[role="dialog"][aria-labelledby="recover-set-title"] .input, [role="dialog"][aria-labelledby="recover-set-title"] .rset__rows, [role="dialog"][aria-labelledby="recover-set-title"] .anotice')).toEqual([]);
    await dlg.getByRole("checkbox", { name: /I accept the cost/ }).check();
    await go(page).click();
    await expect(dlg.getByTestId("result")).toBeVisible({ timeout: 15_000 });
    expect(await measureContrast(page, `recover set dialog (result), ${theme}`, '[role="dialog"][aria-labelledby="recover-set-title"]')).toEqual([]);
    expect(await measureBorders(page, `recover set result, ${theme}`, '[role="dialog"][aria-labelledby="recover-set-title"] .rset__rows')).toEqual([]);
  });

  test(`#1106 contrast, ${theme}: the pre-flight table and the running view`, async ({ page }) => {
    await openDialog(page, "assurance-set-slow");
    await setTheme(page, theme);
    const dlg = dialog(page);
    await toFolder(page, "restore-1");
    await expect(dlg.getByTestId("preflight-summary")).toBeVisible();
    expect(await measureContrast(page, `recover set dialog (pre-flight), ${theme}`, '[role="dialog"][aria-labelledby="recover-set-title"]')).toEqual([]);
    expect(await measureBorders(page, `recover set pre-flight, ${theme}`, '[role="dialog"][aria-labelledby="recover-set-title"] .rset__rows, [role="dialog"][aria-labelledby="recover-set-title"] .input')).toEqual([]);
    await go(page).click();
    await expect(dlg.getByTestId("running")).toBeVisible();
    await expect(dlg.getByTestId("running-lines").locator("li")).toHaveCount(1, { timeout: 8000 });
    expect(await measureContrast(page, `recover set dialog (running), ${theme}`, '[role="dialog"][aria-labelledby="recover-set-title"]')).toEqual([]);
    await dlg.getByRole("button", { name: "Cancel the recovery" }).click();
    await expect(dlg.getByTestId("result")).toBeVisible({ timeout: 8000 });
  });
}
