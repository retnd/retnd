/**
 * Issue #1190, screen 1 of 3: a backup's own page, Recover artifact.
 *
 * The two choices here must be the shared folder field and the shared
 * destination drop-down, under the same words every other screen uses. One
 * backup's target is folder/name, so "Use this folder" gives the folder plus
 * the backup's own file name.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, row } from "./helpers";
import { browseUnavailable, sharedFieldsMeetAA, destinationIsSharedDropDown, noneConfigured, offersBothChoices, stagingIsSharedFolderField } from "./1190-pickers";

const PG = "postgres-prod-20260828.dump.zst";

async function dialog(page: Page, scenario = "default") {
  await open(page, "/backups", scenario);
  await row(page, PG).click();
  await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
  const dlg = page.getByRole("dialog", { name: "Recover artifact" });
  await expect(dlg).toBeVisible();
  return dlg;
}
const action = (p: Page) => p.getByRole("dialog", { name: "Recover artifact" }).getByRole("button", { name: "Recover artifact", exact: true });

test.describe("#1190 Backup detail, Recover artifact", () => {
  test("offers both choices under the shared words", async ({ page }) => {
    await offersBothChoices(await dialog(page));
  });

  test("the staging choice is the shared folder field with the host browser", async ({ page }) => {
    await stagingIsSharedFolderField(await dialog(page), "daily/2026-10/" + PG);
  });

  test("the destination choice is the shared drop-down of Settings' destinations", async ({ page }) => {
    const dlg = await dialog(page);
    await destinationIsSharedDropDown(dlg, dlg);
  });

  test("says None configured when Settings has no destination", async ({ page }) => {
    await noneConfigured(await dialog(page, "no-destinations"), action(page));
  });

  test("a host that cannot list its folders still takes a typed name", async ({ page }) => {
    await browseUnavailable(await dialog(page, "host-browse-unavailable"), page.getByRole("dialog", { name: "Recover artifact" }));
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the shared fields meet WCAG AA in ${theme}`, async ({ page }) => {
      await sharedFieldsMeetAA(page, await dialog(page), '[role="dialog"]', theme);
    });
  }
});
