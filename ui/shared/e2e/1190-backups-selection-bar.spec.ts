/**
 * Issue #1190, screen 2 of 3: the Backups list, the bar a selection opens.
 *
 * The same shared folder field and destination drop-down as the other two
 * screens. A selection goes into a folder, so "Use this folder" gives the
 * folder as it stands.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";
import { browseUnavailable, sharedFieldsMeetAA, destinationIsSharedDropDown, noneConfigured, offersBothChoices, stagingIsSharedFolderField } from "./1190-pickers";

const bar = (p: Page) => p.getByRole("region", { name: "Selected backups" });
const action = (p: Page) => bar(p).getByRole("button", { name: "Recover selected" });

async function tickTwo(page: Page, scenario = "backups-many") {
  await open(page, "/backups", scenario);
  const boxes = page.getByRole("checkbox", { name: /^Select (?!all)/ });
  await boxes.nth(0).check();
  await boxes.nth(1).check();
  await expect(bar(page)).toBeVisible();
  return bar(page);
}

test.describe("#1190 Backups list, selection bar", () => {
  test("offers both choices under the shared words", async ({ page }) => {
    await offersBothChoices(await tickTwo(page));
  });

  test("the staging choice is the shared folder field with the host browser", async ({ page }) => {
    await stagingIsSharedFolderField(await tickTwo(page), "daily/2026-10");
  });

  test("the destination choice is the shared drop-down of Settings' destinations", async ({ page }) => {
    const scope = await tickTwo(page);
    await destinationIsSharedDropDown(scope, scope.getByTestId("recover-command"));
  });

  test("says None configured when Settings has no destination", async ({ page }) => {
    await noneConfigured(await tickTwo(page, "no-destinations"), action(page));
  });

  test("a host that cannot list its folders still takes a typed name", async ({ page }) => {
    await browseUnavailable(await tickTwo(page, "host-browse-unavailable"), bar(page).getByTestId("recover-command"));
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the shared fields meet WCAG AA in ${theme}`, async ({ page }) => {
      await sharedFieldsMeetAA(page, await tickTwo(page), '[data-testid="selection-bar"]', theme);
    });
  }
});
