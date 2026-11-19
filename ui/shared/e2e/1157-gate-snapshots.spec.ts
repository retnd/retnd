import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

/**
 * Issue #1157, the snapshots list, as #1164 left it.
 *
 * Verify latest used to be refused by a shut destructive gate, and this spec
 * pinned the notice that said so and the terminal command it echoed. #1164
 * took verify off the gate, so the same press on the same shut gate now starts
 * a verification, and the screen says nothing about a gate at all. The scenario
 * is unchanged (`gate-shut` still shuts everything that is still behind the
 * gate), which is what makes this a real check: a screen that kept drawing the
 * old notice, or a mock that kept refusing, would fail the first case.
 */
const PAGE = "/sets/production/postgres-primary/snapshots";
const refusal = (page: Page) => page.locator("[data-gate-refusal]");
const notice = (page: Page) => page.locator("[data-gate-notice]");

test.describe("#1157 snapshots list, gate shut (as #1164 left it)", () => {
  test("Verify latest starts a verification and no notice or refusal is drawn", async ({ page }) => {
    await open(page, PAGE, "gate-shut");
    await expect(page.getByRole("button", { name: "Verify latest" })).toBeVisible();
    await expect(notice(page)).toHaveCount(0);
    await page.getByRole("button", { name: "Verify latest" }).click();
    await expect(page.getByRole("region", { name: "Verification in progress" })).toBeVisible();
    await expect(refusal(page)).toHaveCount(0);
  });

  test("control: with the gate open the same press does the same thing", async ({ page }) => {
    await open(page, PAGE, "default");
    await page.getByRole("button", { name: "Verify latest" }).click();
    await expect(page.getByRole("region", { name: "Verification in progress" })).toBeVisible();
    await expect(refusal(page)).toHaveCount(0);
  });
});
