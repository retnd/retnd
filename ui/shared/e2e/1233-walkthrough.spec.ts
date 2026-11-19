/**
 * Slow passes over #1233 for the GIFs in the PR, one set per theme: the
 * Settings card and moving a validator to the workflow environment, the same
 * choice on a deployment whose gate is shut, and a backup's page. Each ends on
 * a DOM assertion about what it shows. Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, setTheme } from "./helpers";

const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);
test.describe.configure({ timeout: 150_000 });
const WORKFLOW = "Workflow environment, secrets included";

for (const theme of ["light", "dark"] as const) {
  test(`#1233 walkthrough (${theme}): Settings, then any-tar moves to the workflow environment`, async ({ page }) => {
    await open(page, "/settings", "validators-env-workflow");
    const card = page.getByRole("region", { name: "Recovery-test validators" });
    await card.scrollIntoViewIfNeeded();
    await expect(card).toBeVisible({ timeout: 30_000 });
    // After the app has booted: a theme set earlier is overwritten by its start-up.
    await setTheme(page, theme);
    await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
    await beat(page, 3000);
    await card.getByRole("button", { name: "Edit any-tar" }).click();
    const dlg = page.getByRole("dialog", { name: "Edit validator" });
    const group = dlg.getByRole("radiogroup", { name: "Environment" });
    await group.scrollIntoViewIfNeeded();
    await beat(page, 2500);
    await group.getByRole("radio", { name: WORKFLOW }).check();
    await beat(page, 2500);
    await dlg.getByRole("button", { name: "Save changes" }).click();
    const env = card.getByRole("row").filter({ hasText: "any-tar" }).locator("[data-validator-env]");
    await expect(env).toHaveAttribute("data-validator-env", "workflow");
    await card.scrollIntoViewIfNeeded();
    await beat(page, 3500);
  });

  test(`#1233 walkthrough (${theme}): workflow on a deployment whose gate is shut`, async ({ page }) => {
    await open(page, "/settings", "gateway-profile");
    const card = page.getByRole("region", { name: "Recovery-test validators" });
    await expect(card).toBeVisible({ timeout: 30_000 });
    await setTheme(page, theme);
    await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
    await card.getByRole("button", { name: "Add validator" }).click();
    const dlg = page.getByRole("dialog", { name: "Add validator" });
    await dlg.getByLabel("Name", { exact: true }).fill("postgres-dump");
    await beat(page, 1500);
    const group = dlg.getByRole("radiogroup", { name: "Environment" });
    await group.scrollIntoViewIfNeeded();
    await beat(page, 2000);
    await group.getByRole("radio", { name: WORKFLOW }).check();
    await expect(dlg.locator("[data-env-gate]")).toBeVisible();
    await dlg.locator("[data-env-gate]").scrollIntoViewIfNeeded();
    await beat(page, 4500);
  });

  test(`#1233 walkthrough (${theme}): a backup's page names the environment`, async ({ page }) => {
    await open(page, "/backups/production/billing-mysql/billing-20260827.sql.gz", "validators-env-workflow");
    const region = page.getByRole("region", { name: "Assurance" });
    await expect(region.locator("[data-validator-env]")).toBeVisible({ timeout: 30_000 });
    await setTheme(page, theme);
    await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
    await region.scrollIntoViewIfNeeded();
    await beat(page, 4000);
    await expect(region.locator("[data-validator-env]")).toContainText(WORKFLOW);
  });
}
