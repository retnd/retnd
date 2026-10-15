/**
 * Slow, watchable passes over the #1191 dialogs, for the GIFs on the issue
 * (before) and the PR (after). Each ends on a DOM assertion about what it
 * shows. Run with E2E_VIDEO=1.
 *
 * After #1193's review the pass shows: the dialog opens on the source host
 * and has written nothing yet; the tick runs the write check and names the
 * bytes; a set declared read_only starts on the staging folder with the
 * reason; and a set with no SFTP source starts on the download.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, row } from "./helpers";
import { ACK, SOURCE, STAGING } from "./1191-source-host";

const PG = "postgres-prod-20260828.dump.zst";
const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);

test("#1191 walkthrough: every recovery dialog starts on the backup's source where it can", async ({ page }) => {
  await open(page, "/backups", "default");
  await beat(page);
  await row(page, PG).click();
  await beat(page, 600);
  await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
  const dlg = page.getByRole("dialog", { name: "Recover artifact" });
  await expect(dlg).toBeVisible();
  await expect(dlg.getByRole("radio", { name: SOURCE })).toBeChecked();
  await expect(dlg.getByTestId("source-preflight")).toHaveAttribute("data-write-access", "not_checked");
  await beat(page, 2500);
  await dlg.getByRole("checkbox", { name: ACK }).check();
  await expect(dlg.getByTestId("source-preflight")).toHaveAttribute("data-write-access", "writable");
  await expect(dlg.getByTestId("source-space")).toContainText("free");
  await beat(page, 3000);

  await open(page, "/sets/production/postgres-primary", "source-set-read-only");
  await beat(page);
  await page.getByRole("button", { name: "Recover this backup set", exact: true }).click();
  const set = page.getByRole("dialog", { name: "Recover this backup set" });
  await expect(set.getByRole("radio", { name: STAGING })).toBeChecked();
  await expect(set.getByTestId("source-unavailable")).toContainText("read_only");
  await beat(page, 3000);

  await open(page, "/backups/production/postgres-primary/" + PG, "source-not-sftp");
  await beat(page);
  await page.getByRole("region", { name: "Assurance" }).getByRole("button", { name: "Recover artifact" }).click();
  await expect(page.getByTestId("source-unavailable")).toContainText("not SFTP");
  await beat(page, 3000);
});
