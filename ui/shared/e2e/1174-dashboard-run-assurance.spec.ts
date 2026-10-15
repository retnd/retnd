import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1174: the Dashboard's Assurance tile counted Due, Overdue and
 * Degraded but never said which backups they were, and starting the assurance
 * run meant leaving the Dashboard. The tile now lists those backups, one row
 * each, with a Run assurance button that starts the same recovery test the
 * backup set page starts (POST .../recovery-test) and refreshes the counts
 * when the run ends.
 *
 * The default fixture: media-week34 has never been checked (due), billing's
 * recovery test could not complete and redis has no readable copy (degraded).
 * A mock recovery test takes about 25 seconds, so the specs that wait for the
 * end carry their own timeout.
 */
const card = (page: Page) => page.getByRole("region", { name: "Assurance summary" });
const list = (page: Page) => card(page).getByRole("list", { name: "Backups needing assurance" });
const rowOf = (page: Page, file: string): Locator => list(page).getByRole("listitem").filter({ hasText: file });

test.describe("#1174 the Assurance tile lists what is due and runs it per row", () => {
  test("#1174 every due, overdue or degraded backup is a row with its set, why and a Run assurance button", async ({ page }) => {
    await open(page, "/");
    await expect(card(page).getByTestId("assurance-degraded")).toContainText("2");
    await expect(list(page).getByRole("listitem")).toHaveCount(3);
    const billing = rowOf(page, "billing-20260827.sql.gz");
    await expect(billing).toContainText("production/billing-mysql");
    await expect(billing).toContainText(/degraded/i);
    await expect(billing).toContainText(/could not complete/i);
    await expect(billing.getByRole("button", { name: "Run assurance for billing-20260827.sql.gz" })).toBeEnabled();
    await expect(rowOf(page, "media-week34.tar")).toContainText(/due/i);
    await expect(rowOf(page, "redis-20260812.rdb.zst")).toContainText(/degraded/i);
  });

  test("#1174 pressing a row's button shows that row in flight, then the tile counts refresh when the run ends", async ({ page }) => {
    test.setTimeout(120_000);
    await open(page, "/");
    await expect(card(page).getByTestId("assurance-degraded")).toContainText("2");
    const billing = rowOf(page, "billing-20260827.sql.gz");
    await billing.getByRole("button", { name: /Run assurance for billing/ }).click();
    await expect(billing).toHaveAttribute("data-state", "running");
    await expect(billing.getByRole("status")).toContainText(/running|stage/i);
    await expect(billing.getByRole("button", { name: /Run assurance for billing/ }), "one run per row at a time").toBeDisabled();
    // The other rows are untouched.
    await expect(rowOf(page, "redis-20260812.rdb.zst")).not.toHaveAttribute("data-state", "running");
    await expect(card(page).getByTestId("assurance-degraded"), "the tile has not moved while the run is in flight").toContainText("2");
    await expect(card(page).getByTestId("assurance-degraded")).toContainText("1", { timeout: 60_000 });
    // The row it ran stays, saying how it ended, so the operator is not left
    // watching a row vanish.
    await expect(billing).toHaveAttribute("data-state", "complete");
    await expect(billing.getByRole("status")).toContainText(/finished/i);
  });

  test("#1174 a set that can only refuse a test gets a disabled button and the reason beside it", async ({ page }) => {
    await open(page, "/", "assurance-no-validator", { wire: true });
    const redis = rowOf(page, "redis-20260812.rdb.zst");
    await expect(redis).toBeVisible();
    const run = redis.getByRole("button", { name: /Run assurance for redis/ });
    await expect(run, "a button that can only refuse must not be offered").toBeDisabled();
    await expect(redis).toContainText(/no recovery-test validator is declared/i);
  });

  test("#1174 control: with the destructive gate shut the button is still offered, because a recovery test is not a gated act", async ({ page }) => {
    await open(page, "/", "gate-shut-local-account");
    const billing = rowOf(page, "billing-20260827.sql.gz");
    await expect(billing.getByRole("button", { name: /Run assurance for billing/ })).toBeEnabled();
    await billing.getByRole("button", { name: /Run assurance for billing/ }).click();
    await expect(billing).toHaveAttribute("data-state", "running");
    await expect(page.locator("[data-gate-refusal]")).toHaveCount(0);
  });

  test("#1174 the terminal twin of the button is printed", async ({ page }) => {
    await open(page, "/");
    await expect(list(page)).toBeVisible();
    await expect(card(page)).toContainText("retnd recovery-test");
  });

  for (const theme of ["light", "dark"] as const) {
    test("#1174 the list is readable in the " + theme + " theme (WCAG AA)", async ({ page }) => {
      await open(page, "/");
      await expect(list(page)).toBeVisible();
      await setTheme(page, theme);
      expect(await measureContrast(page, "1174 due list " + theme, '[aria-label="Backups needing assurance"]')).toEqual([]);
    });
  }
});
