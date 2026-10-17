/**
 * Slow passes over #1203 for the GIFs in the PR, one per theme and per screen.
 * Each ends on a DOM assertion about the state it shows. Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, setTheme } from "./helpers";

const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);

// The first page of a run compiles the app, and these passes are slow on
// purpose; the default 30 s also covers the page fixture's own setup.
test.describe.configure({ timeout: 150_000 });
const POSTGRES = String.raw`^postgres-prod-\d{8}\.dump\.zst$`;
const PASSWORD = "correct-horse-battery";

for (const theme of ["light", "dark"] as const) {
  test(`#1203 walkthrough (${theme}): Settings, pick a suggested pattern, upload, password`, async ({ page }) => {
    await open(page, "/settings", "local-account-profile");
    const card = page.getByRole("region", { name: "Recovery-test validators" });
    await card.scrollIntoViewIfNeeded();
    await expect(card).toBeVisible({ timeout: 30_000 });
    // After the app has booted: the mock now loads lazily, and a theme set
    // before that is overwritten by the app's own start-up.
    await setTheme(page, theme);
    await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
    await beat(page, 1800);
    await card.getByRole("button", { name: "Add validator" }).click();
    const dlg = page.getByRole("dialog", { name: "Add validator" });
    await beat(page);
    await dlg.getByLabel("Name", { exact: true }).pressSequentially("postgres-dump", { delay: 60 });
    await dlg.getByRole("radio", { name: POSTGRES }).check();
    await beat(page, 1500);
    await dlg.getByRole("button", { name: "Start from the default script" }).click();
    await beat(page, 1500);
    await dlg.getByLabel("Your password").pressSequentially("not-the-password", { delay: 50 });
    await dlg.getByRole("button", { name: "Upload validator" }).click();
    await expect(dlg.locator("[data-validator-error]")).toContainText("That password was not accepted");
    await beat(page, 2000);
    await dlg.getByLabel("Your password").pressSequentially(PASSWORD, { delay: 50 });
    await dlg.getByRole("button", { name: "Upload validator" }).click();
    await expect(card.getByRole("row").filter({ hasText: "postgres-dump" })).toHaveCount(1);
    await beat(page, 3000);
  });

  test(`#1203 walkthrough (${theme}): a backup's page, Declare validator, a pattern the engine refuses`, async ({ page }) => {
    await open(page, "/backups/production/postgres-primary/postgres-prod-20260828.dump.zst");
    const region = page.getByRole("region", { name: "Assurance" });
    await expect(region).toBeVisible({ timeout: 30_000 });
    // After the app has booted, as above.
    await setTheme(page, theme);
    await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
    await beat(page, 1500);
    await region.getByRole("button", { name: "Declare validator" }).click();
    const dlg = page.getByRole("dialog", { name: "Add validator" });
    await expect(dlg.getByRole("radio", { name: POSTGRES })).toBeChecked();
    await beat(page, 2500);
    await dlg.getByLabel("Artifact-name pattern").fill("");
    await dlg.getByLabel("Artifact-name pattern").pressSequentially("^(?=postgres)", { delay: 70 });
    await expect(dlg.locator("[data-validator-preview]").getByRole("alert")).toContainText("invalid or unsupported Perl syntax");
    await beat(page, 2500);
    await dlg.getByRole("radio", { name: POSTGRES }).check();
    await dlg.getByLabel("Name", { exact: true }).pressSequentially("postgres-dump", { delay: 60 });
    await dlg.getByRole("button", { name: "Start from the default script" }).click();
    await beat(page, 1500);
    await dlg.getByRole("button", { name: "Upload validator" }).click();
    await expect(region.locator("[data-validator-declared]")).toContainText("Uploaded postgres-dump");
    await beat(page, 3000);
  });
}
