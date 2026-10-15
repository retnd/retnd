/**
 * Slow, watchable passes over the #1191 dialogs, for the GIFs on the issue
 * (before) and the PR (after). Each ends on a DOM assertion about what it
 * shows. Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, row } from "./helpers";
import { SOURCE } from "./1191-source-host";

const PG = "postgres-prod-20260828.dump.zst";
const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);

test("#1191 walkthrough: every recovery dialog starts on the backup's source", async ({ page }) => {
  await open(page, "/backups", "default");
  await beat(page);
  await row(page, PG).click();
  await beat(page, 600);
  await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
  const dlg = page.getByRole("dialog", { name: "Recover artifact" });
  await expect(dlg).toBeVisible();
  await beat(page, 2500);
  await expect(dlg.getByRole("radio", { name: SOURCE })).toBeChecked();
  await dlg.getByRole("checkbox", { name: /I understand this writes onto/ }).check();
  await beat(page, 2000);

  await open(page, "/sets/production/postgres-primary", "source-read-only");
  await beat(page);
  await page.getByRole("button", { name: "Recover this backup set", exact: true }).click();
  const set = page.getByRole("dialog", { name: "Recover this backup set" });
  await expect(set.getByTestId("source-preflight")).toContainText("read-only");
  await beat(page, 3000);

  await open(page, "/backups/production/postgres-primary/" + PG, "source-not-sftp");
  await beat(page);
  await page.getByRole("region", { name: "Assurance" }).getByRole("button", { name: "Recover artifact" }).click();
  await expect(page.getByTestId("source-unavailable")).toContainText("not SFTP");
  await beat(page, 3000);
});
