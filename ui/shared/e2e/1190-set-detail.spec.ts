/**
 * Issue #1190, screen 3 of 3: a backup set's page, Recover this backup set.
 *
 * The same shared folder field and destination drop-down as the other two
 * screens. A set goes into a folder, so "Use this folder" gives the folder as
 * it stands.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";
import { browseUnavailable, sharedFieldsMeetAA, destinationIsSharedDropDown, noneConfigured, offersBothChoices, stagingIsSharedFolderField, destinationNamesDiffer, meaningSays } from "./1190-pickers";

// The one artifact-engine set in every scenario (the others are incremental
// outside the assurance-set- scenarios, and an incremental set has a snapshot
// restore instead of these choices).
const SET = "/sets/production/auth-config";
const dlg = (p: Page) => p.getByRole("dialog", { name: "Recover this backup set" });
const action = (p: Page) => dlg(p).getByRole("button", { name: "Recover this backup set", exact: true });

async function dialog(page: Page, scenario = "default") {
  await open(page, SET, scenario);
  await page.getByRole("button", { name: "Recover this backup set", exact: true }).click();
  await expect(dlg(page)).toBeVisible();
  return dlg(page);
}

test.describe("#1190 Set detail, Recover this backup set", () => {
  test("offers both choices under the shared words", async ({ page }) => {
    await offersBothChoices(await dialog(page));
  });

  test("the line under the staging field says the name is a folder", async ({ page }) => {
    await meaningSays(await dialog(page), "A folder");
  });

  test("the destination radio and its drop-down have different names", async ({ page }) => {
    await destinationNamesDiffer(await dialog(page));
  });

  test("the staging choice is the shared folder field with the host browser", async ({ page }) => {
    await stagingIsSharedFolderField(await dialog(page), "daily/2026-10");
  });

  test("the destination choice is the shared drop-down of Settings' destinations", async ({ page }) => {
    const scope = await dialog(page);
    await destinationIsSharedDropDown(scope, scope);
  });

  test("says None configured when Settings has no destination", async ({ page }) => {
    await noneConfigured(await dialog(page, "no-destinations"), action(page));
  });

  test("a host that cannot list its folders still takes a typed name", async ({ page }) => {
    await browseUnavailable(await dialog(page, "host-browse-unavailable"), dlg(page));
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the shared fields meet WCAG AA in ${theme}`, async ({ page }) => {
      await sharedFieldsMeetAA(page, await dialog(page), '[role="dialog"]', theme);
    });
  }
});
