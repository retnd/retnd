/**
 * Slow passes over the #1111 chip for the GIFs in the PR, one per theme. Each
 * ends on a DOM assertion about the state it shows. Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, setTheme } from "./helpers";

const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);

for (const theme of ["light", "dark"] as const) {
  test(`#1111 walkthrough (${theme}): run the recovery test, the chip reads Recovered, readable`, async ({ page }) => {
    test.setTimeout(150_000);
    await open(page, "/sets/production/postgres-primary", "assurance-builtin-check", { wire: true });
    const section = page.getByRole("region", { name: "Assurance" });
    const recovery = section.locator('[data-claim="recovery"]');
    const log = page.getByLabel("Terminal", { exact: true }).getByRole("log");
    await expect(recovery).toHaveAttribute("data-state", "never_configured");
    await setTheme(page, theme);
    await recovery.scrollIntoViewIfNeeded();
    await beat(page, 1800);
    await section.getByRole("button", { name: "Run recovery test" }).click();
    await expect(recovery).toHaveAttribute("data-state", "pass_builtin", { timeout: 60_000 });
    await expect(log.getByText("recovered and readable: postgres-prod-20260829.tar.zst (built-in check)")).toBeVisible();
    await expect(recovery).toContainText("Recovered, readable");
    await expect(recovery).not.toContainText("Recovery test passed");
    await expect(log).not.toContainText("recovery test passed");
    await beat(page, 3500);
  });
}
