import { expect, test } from "@playwright/test";
import { open, row, setTheme, spaGo } from "./helpers";

/**
 * Issues #985 and #1070: one pass through the visual-honesty fixes, in the
 * order an operator meets them, with an assertion on the page at every step.
 * It is the flow the PR's GIF is cut from (E2E_VIDEO=1) and a real test too,
 * since a recording of a page nobody checked proves nothing.
 *
 * The Backups list with its footer and the chips (a Content chip that covers
 * one copy of two, the Not configured edge), in both themes; then the artifact
 * page for each way a download can end.
 */
test("#985 #1070 the footer, the chips and the three download outcomes, in one pass", async ({ page }) => {
  await open(page, "/backups", "assurance-download-outcomes", { wire: true });
  const pg = row(page, "postgres-prod-20260828.dump.zst");
  await expect(pg.locator('[data-claim="content"]')).toContainText("Content verified · 1 of 2 copies, 1 existence only");
  await expect(pg.locator('[data-claim="content"]')).toHaveAttribute("data-coverage", "partial");
  await expect(row(page, "media-week34.tar").locator('[data-claim="recovery"]')).toHaveAttribute("data-state", "never_configured");
  const footer = page.getByTestId("backups-boundary");
  await footer.scrollIntoViewIfNeeded();
  await expect(footer).toContainText("restoring it into an application remains the operator's responsibility");
  await page.waitForTimeout(1500);

  await setTheme(page, "dark");
  await expect(footer).toBeVisible();
  await page.waitForTimeout(1500);
  await setTheme(page, "light");

  const cases = [
    { path: "/backups/production/postgres-primary/postgres-prod-20260828.dump.zst", outcome: "completed", badge: "Completed" },
    { path: "/backups/production/billing-mysql/billing-20260827.sql.gz", outcome: "interrupted", badge: "Interrupted at 1200 of 5000 bytes" },
    { path: "/backups/production/auth-config/auth-config-20260826.tar.zst", outcome: "failed_verification", badge: "Failed verification" }
  ];
  for (const c of cases) {
    await spaGo(page, c.path);
    const box = page.getByTestId("download-outcome");
    await expect(box).toHaveAttribute("data-outcome", c.outcome);
    await expect(box.locator("[data-badge]")).toHaveText(c.badge);
    await expect(box.locator('[data-state="pass"]')).toHaveCount(0);
    await box.scrollIntoViewIfNeeded();
    await page.waitForTimeout(1800);
  }
  await setTheme(page, "dark");
  await expect(page.getByTestId("download-outcome")).toBeVisible();
  await page.waitForTimeout(1500);
});
