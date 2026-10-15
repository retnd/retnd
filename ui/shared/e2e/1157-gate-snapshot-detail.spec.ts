import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

/**
 * Issue #1157, one snapshot, as #1164 left it.
 *
 * Verify now and Place hold used to be refused by a shut destructive gate and
 * echoed the terminal command. #1164 took verify, hold and release-hold off
 * the gate, so on a shut gate both now work and neither draws a notice. The
 * Restore control on this page still leads to a gated screen, which has its
 * own spec (1157-gate-snapshot-restore.spec.ts).
 */
const RUN = "run-2026-09-13-0400";
const PAGE = "/sets/production/postgres-primary/snapshots/" + RUN;
const refusal = (page: Page) => page.locator("[data-gate-refusal]");
const notice = (page: Page) => page.locator("[data-gate-notice]");

test.describe("#1157 snapshot detail, gate shut (as #1164 left it)", () => {
  test("Verify now starts a verification", async ({ page }) => {
    await open(page, PAGE, "gate-shut");
    await expect(page.getByRole("button", { name: "Verify now" })).toBeVisible();
    await expect(notice(page)).toHaveCount(0);
    await page.getByRole("button", { name: "Verify now" }).click();
    await expect(page.getByRole("region", { name: "Verification in progress" })).toBeVisible();
    await expect(refusal(page)).toHaveCount(0);
  });

  test("Place hold places the hold with the reason that was typed", async ({ page }) => {
    await open(page, PAGE, "gate-shut");
    await page.getByRole("button", { name: /Place hold/ }).click();
    await page.getByLabel("Reason").fill("legal-review");
    await page.getByRole("button", { name: "Place hold", exact: true }).click();
    await expect(page.getByText("legal-review")).toBeVisible();
    await expect(refusal(page)).toHaveCount(0);
  });

  test("the Restore control is still there, and the gate is still said on the screen it leads to", async ({ page }) => {
    await open(page, PAGE, "gate-shut");
    await page.getByRole("button", { name: /Restore/ }).click();
    await expect(notice(page)).toBeVisible();
  });
});
