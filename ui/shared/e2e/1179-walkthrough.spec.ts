/**
 * Slow, watchable passes over the #1179 behaviour, for the GIFs in the PR.
 * Each one ends on a DOM assertion about the state it shows, so a GIF that
 * looks right cannot be a page that is not. Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, row } from "./helpers";

const PG = "postgres-prod-20260828.dump.zst";
const bar = (p: Page) => p.getByRole("region", { name: "Selected backups" });
const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);

async function recoverDialog(page: Page, scenario = "default") {
  await open(page, "/backups", scenario);
  await beat(page);
  await row(page, PG).click();
  await beat(page, 600);
  await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
  const dlg = page.getByRole("dialog", { name: "Recover artifact" });
  await expect(dlg).toBeVisible();
  await beat(page);
  return dlg;
}

test("#1179 walkthrough: Download in this browser is selectable and hands over one download per backup", async ({ page }) => {
  await open(page, "/backups", "backups-many");
  await beat(page);
  await page.getByRole("checkbox", { name: "Select postgres-primary-20260801.bak", exact: true }).check();
  await beat(page, 500);
  await page.getByRole("checkbox", { name: "Select billing-mysql-20260802.bak", exact: true }).check();
  await beat(page);
  const radio = bar(page).getByRole("radio", { name: "Download in this browser" });
  await expect(radio).toBeEnabled();
  await radio.check();
  await beat(page, 1500);
  await bar(page).getByRole("button", { name: "Download selected" }).click();
  await expect(page.getByTestId("bulk-download")).toContainText("2 downloads handed to your browser", { timeout: 15_000 });
  await beat(page, 2500);
});

test("#1179 walkthrough: Folder under the staging root on the host browses the host's folders", async ({ page }) => {
  const dlg = await recoverDialog(page);
  await dlg.getByRole("radio", { name: "Folder under the staging root on the host" }).check();
  await beat(page, 1400);
  const browser = dlg.getByRole("group", { name: "Folders on the host" });
  await browser.getByRole("button", { name: "Open folder daily" }).click();
  await beat(page, 1100);
  await browser.getByRole("button", { name: "Open folder 2026-10" }).click();
  await beat(page, 1100);
  await browser.getByRole("button", { name: "Use this folder" }).click();
  await expect(dlg.getByRole("textbox", { name: /Name under the staging root/ })).toHaveValue("daily/2026-10/" + PG);
  await beat(page, 2500);
});

test("#1179 walkthrough: Configured destination is a drop-down of Settings", async ({ page }) => {
  const dlg = await recoverDialog(page);
  await dlg.getByRole("radio", { name: "Configured destination" }).check();
  await beat(page, 1400);
  const select = dlg.getByRole("combobox", { name: "Configured destination" });
  await expect(select.locator("option")).toHaveCount(5);
  await select.selectOption("offsite_s3");
  await expect(dlg.getByText("--to medium:offsite_s3", { exact: false })).toBeVisible();
  await beat(page, 2500);
});

test("#1179 walkthrough: with no destination in Settings it says None configured", async ({ page }) => {
  const dlg = await recoverDialog(page, "no-destinations");
  await dlg.getByRole("radio", { name: "Configured destination" }).check();
  const select = dlg.getByRole("combobox", { name: "Configured destination" });
  await expect(select).toContainText("None configured");
  await expect(select).toBeDisabled();
  await beat(page, 2500);
});
