/**
 * Slow, watchable passes over the #1107 behaviour, for the GIFs in the PR.
 * Each one ends on a DOM assertion about the state it shows, so a GIF that
 * looks right cannot be a page that is not. Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const bar = (p: Page) => p.getByRole("region", { name: "Selected backups" });
const headerBox = (p: Page) => p.getByRole("checkbox", { name: "Select all matching backups" });
const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);

async function staging(p: Page, name: string) {
  await bar(p).getByRole("radio", { name: "Under the staging root on this retnd host" }).check();
  await bar(p).getByRole("textbox", { name: /Name under the staging root/ }).fill(name);
}

test("#1107 walkthrough: filter by date, select all matching, recover to one place", async ({ page }) => {
  await open(page, "/backups", "backups-many");
  await beat(page);
  await page.getByLabel("Received on or after").fill("2026-08-20");
  await page.getByLabel("Received on or before").fill("2026-08-24");
  await beat(page);
  await headerBox(page).check();
  await beat(page);
  await expect(bar(page).getByTestId("selection-count")).toContainText("5 selected of 5 matching");
  await staging(page, "august-drill");
  await beat(page, 1500);
  await bar(page).getByRole("button", { name: "Recover selected" }).click();
  await expect(page.getByTestId("run-summary")).toContainText("5 recovered", { timeout: 15_000 });
  await beat(page, 2000);
});

test("#1107 walkthrough: changing the filter clears the selection and says so", async ({ page }) => {
  await open(page, "/backups", "backups-many");
  await beat(page);
  await headerBox(page).check();
  await beat(page, 1500);
  await page.getByRole("combobox", { name: "Filter by backup set" }).selectOption({ label: "Auth service config" });
  await expect(page.getByTestId("selection-notice")).toContainText("selection was cleared");
  await expect(bar(page)).toHaveCount(0);
  await beat(page, 2000);
});

test("#1107 walkthrough: one corrupt copy fails, the rest still arrive", async ({ page }) => {
  await open(page, "/backups", "backups-many-corrupt");
  await beat(page);
  await page.getByLabel("Received on or after").fill("2026-08-03");
  await page.getByLabel("Received on or before").fill("2026-08-07");
  await headerBox(page).check();
  await beat(page);
  await staging(page, "drill-with-a-bad-copy");
  await beat(page, 1200);
  await bar(page).getByRole("button", { name: "Recover selected" }).click();
  await expect(page.getByTestId("run-summary")).toContainText("4 recovered", { timeout: 15_000 });
  await expect(page.getByTestId("run-summary")).toContainText("1 failed");
  await expect(page.getByRole("alert")).toContainText("billing-mysql-20260805.bak");
  await beat(page, 2500);
});
