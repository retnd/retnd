import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { measureContrast, measureEdges, open, row, setTheme } from "./helpers";

/**
 * Issue #1202: every row of the Backups page has an Actions menu that runs the
 * per-backup commands without opening the backup's own page. Each item does
 * what the control it mirrors does elsewhere, through the same code:
 *
 *   - Recover artifact opens the backup page's Recover artifact dialog;
 *   - Run recovery test is the Dashboard row's start (`startRecoveryRun`), so
 *     it names this backup, the same as `retnd recovery-test SET --artifact ID`;
 *   - Scrub this backup's set is the set page's Scrub now (`startScrub`), and
 *     says it reads the whole set, because that is what a scrub is;
 *   - Retry ingestion is the backup page's Recovery card, offered on exactly
 *     the backups that card is drawn for (FAILED and not quarantined).
 *
 * An item that cannot run stays in the menu, disabled, with the reason. A run
 * the menu started is shown above the table with its progress, Cancel and the
 * terminal twin. A mock scrub takes about 4 seconds and a recovery test about
 * 25, so the specs that wait for the end carry their own timeout.
 */
const BILLING = "billing-20260827.sql.gz";
const BILLING_ID = "production/billing-mysql/" + BILLING;
const BILLING_OLD = "billing-20260824.sql.gz";
const actions = (page: Page, file: string) => page.getByRole("button", { name: "Actions for " + file });
const menu = (page: Page) => page.getByRole("menu");
const item = (page: Page, name: string | RegExp) => menu(page).getByRole("menuitem", { name });
const runs = (page: Page) => page.getByRole("region", { name: "Started from this list" });

async function openMenu(page: Page, file: string): Promise<Locator> {
  await actions(page, file).click();
  await expect(menu(page)).toBeVisible();
  return menu(page);
}

test.describe("#1202 the Backups row Actions menu", () => {
  test("#1202 every row has an Actions button that opens a labelled menu and does not open the backup", async ({ page }) => {
    await open(page, "/backups");
    const button = actions(page, BILLING);
    await expect(button).toHaveAttribute("aria-haspopup", "menu");
    await expect(button).toHaveAttribute("aria-expanded", "false");
    await button.click();
    await expect(button).toHaveAttribute("aria-expanded", "true");
    await expect(page.getByRole("menu", { name: "Actions for " + BILLING })).toBeVisible();
    await expect(page, "the row's own click must not fire").toHaveURL(/\/backups\?/);
    const names = (await menu(page).getByRole("menuitem").evaluateAll((els) =>
      els.map((e) => (e.getAttribute("aria-labelledby") ? document.getElementById(e.getAttribute("aria-labelledby")!)?.textContent : e.textContent) ?? "")));
    expect(names).toEqual(["Recover artifact…", "Run recovery test", "Scrub this backup's set", "Open backup page"]);
    // Grouped the way the designs group them, each group named for a screen reader.
    await expect(menu(page).getByRole("group", { name: "Get it back" })).toBeVisible();
    await expect(menu(page).getByRole("group", { name: "Check it" })).toBeVisible();
    await expect(menu(page).getByRole("group", { name: "More" })).toBeVisible();
    await expect(menu(page).getByRole("group", { name: "Repair" }), "nothing to repair on a healthy backup").toHaveCount(0);
    // Every row has one, not only the first.
    for (const file of ["postgres-prod-20260828.dump.zst", "media-week34.tar", "redis-20260812.rdb.zst"]) {
      await expect(actions(page, file)).toBeVisible();
    }
  });

  test("#1202 keyboard: Enter opens on the first item, arrows move and wrap, Home and End jump, Escape returns focus", async ({ page }) => {
    await open(page, "/backups");
    const button = actions(page, BILLING);
    await button.focus();
    await page.keyboard.press("Enter");
    await expect(item(page, /^Recover artifact/)).toBeFocused();
    await page.keyboard.press("ArrowDown");
    await expect(item(page, "Run recovery test")).toBeFocused();
    await page.keyboard.press("End");
    await expect(item(page, "Open backup page")).toBeFocused();
    await page.keyboard.press("ArrowDown");
    await expect(item(page, /^Recover artifact/), "ArrowDown on the last item wraps to the first").toBeFocused();
    await page.keyboard.press("ArrowUp");
    await expect(item(page, "Open backup page"), "ArrowUp on the first item wraps to the last").toBeFocused();
    await page.keyboard.press("Home");
    await expect(item(page, /^Recover artifact/)).toBeFocused();
    await page.keyboard.press("s");
    await expect(item(page, /^Scrub this backup/), "type-ahead jumps to the item starting with the letter").toBeFocused();
    await page.keyboard.press("Escape");
    await expect(menu(page)).toHaveCount(0);
    await expect(button).toBeFocused();
    await expect(button).toHaveAttribute("aria-expanded", "false");
  });

  test("#1202 keyboard: ArrowUp on the button opens on the last item, Tab closes, a click outside closes", async ({ page }) => {
    await open(page, "/backups");
    const button = actions(page, BILLING);
    await button.focus();
    await page.keyboard.press("ArrowUp");
    await expect(item(page, "Open backup page")).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(menu(page)).toHaveCount(0);
    await button.focus();
    await page.keyboard.press("ArrowDown");
    await expect(item(page, /^Recover artifact/)).toBeFocused();
    await page.getByRole("heading", { name: "Backups" }).click();
    await expect(menu(page)).toHaveCount(0);
    await expect(page).toHaveURL(/\/backups\?/);
  });

  test("#1202 a right-click on the row, or Shift+F10 inside it, opens that row's menu instead of the backup", async ({ page }) => {
    await open(page, "/backups");
    await row(page, BILLING).getByRole("cell", { name: "Billing MySQL" }).click({ button: "right" });
    await expect(page.getByRole("menu", { name: "Actions for " + BILLING })).toBeVisible();
    await expect(item(page, /^Recover artifact/)).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(actions(page, BILLING)).toBeFocused();
    await page.getByRole("checkbox", { name: "Select " + BILLING }).focus();
    await page.keyboard.press("Shift+F10");
    await expect(page.getByRole("menu", { name: "Actions for " + BILLING })).toBeVisible();
    await expect(page).toHaveURL(/\/backups\?/);
  });

  test("#1202 the menu's footer prints the terminal twin of the item that has focus", async ({ page }) => {
    await open(page, "/backups");
    await actions(page, BILLING).focus();
    await page.keyboard.press("Enter");
    const echo = menu(page).getByTestId("menu-echo");
    await expect(echo).toContainText("retnd recover " + BILLING_ID + " --to");
    await page.keyboard.press("ArrowDown");
    await expect(echo).toContainText("retnd recovery-test production/billing-mysql --artifact " + BILLING_ID);
    await page.keyboard.press("ArrowDown");
    await expect(echo).toContainText("retnd scrub production/billing-mysql");
    await page.keyboard.press("ArrowDown");
    await expect(echo).toContainText("retnd artifacts " + BILLING_ID);
  });

  test("#1202 Open backup page goes to that backup's own page", async ({ page }) => {
    await open(page, "/backups");
    await actions(page, BILLING).focus();
    await page.keyboard.press("ArrowUp");
    await expect(item(page, "Open backup page")).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/\/backups\/production\/billing-mysql\/billing-20260827\.sql\.gz/);
  });

  test("#1202 Recover artifact opens the backup page's own Recover artifact dialog, and focus comes back to Actions", async ({ page }) => {
    await open(page, "/backups", "default", { wire: true });
    await openMenu(page, BILLING);
    await item(page, /^Recover artifact/).click();
    const dialog = page.getByRole("dialog", { name: "Recover artifact" });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText(BILLING_ID);
    await expect(dialog.getByRole("radiogroup", { name: "Where should the artifact go?" })).toBeVisible();
    await expect(dialog, "the terminal twin is the dialog's own").toContainText("retnd recover " + BILLING_ID);
    await expect(page).toHaveURL(/\/backups\?/);
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    await expect(actions(page, BILLING)).toBeFocused();
  });

  test("#1202 Run recovery test starts a test of that backup, shows it to the end with its terminal twin, and the claims refresh", async ({ page }) => {
    test.setTimeout(120_000);
    await open(page, "/backups", "default", { wire: true });
    const recovery = row(page, BILLING).locator('[data-claim="recovery"]');
    await expect(recovery).not.toHaveAttribute("data-state", "pass");
    await openMenu(page, BILLING);
    await item(page, "Run recovery test").click();
    await expect(menu(page)).toHaveCount(0);
    const panel = runs(page).getByTestId("operation-progress");
    await expect(panel).toHaveAttribute("data-state", "running");
    await expect(runs(page)).toContainText("Recovery test of " + BILLING);
    await expect(runs(page)).toContainText("retnd recovery-test production/billing-mysql --artifact " + BILLING_ID);
    await expect(runs(page).getByRole("button", { name: "Cancel" })).toBeVisible();
    await expect(panel).toHaveAttribute("data-state", "complete", { timeout: 60_000 });
    await expect(recovery, "the row's Recovery claim is re-read when the run ends").toHaveAttribute("data-state", "pass", { timeout: 10_000 });
  });

  test("#1202 while a test runs, another backup of the same set says why it cannot start one", async ({ page }) => {
    test.setTimeout(60_000);
    await open(page, "/backups", "default", { wire: true });
    await openMenu(page, BILLING);
    await item(page, "Run recovery test").click();
    await expect(runs(page).getByTestId("operation-progress")).toHaveAttribute("data-state", "running");
    await openMenu(page, BILLING_OLD);
    const test_ = item(page, "Run recovery test");
    await expect(test_).toHaveAttribute("aria-disabled", "true");
    await expect(test_).toContainText("A recovery test of " + BILLING + " is running for this set");
    await test_.click();
    await expect(menu(page), "a disabled item does nothing and leaves the menu open").toBeVisible();
    // Another set is not held up by it.
    await page.keyboard.press("Escape");
    await openMenu(page, "postgres-prod-20260828.dump.zst");
    await expect(item(page, "Run recovery test")).not.toHaveAttribute("aria-disabled", "true");
  });

  test("#1202 Scrub this backup's set starts the set's scrub, says it reads the whole set, and shows it to the end", async ({ page }) => {
    test.setTimeout(60_000);
    await open(page, "/backups", "default", { wire: true });
    await openMenu(page, BILLING);
    await expect(item(page, /^Scrub this backup/)).toContainText("every copy in Billing MySQL");
    await item(page, /^Scrub this backup/).click();
    await expect(runs(page)).toContainText("Scrub of Billing MySQL");
    await expect(runs(page)).toContainText("retnd scrub production/billing-mysql");
    await expect(runs(page).getByTestId("operation-progress")).toHaveAttribute("data-state", "complete", { timeout: 30_000 });
    await expect(runs(page)).toContainText("Scrub finished");
  });

  test("#1202 a read-only session keeps the items in the menu, disabled, with the reason", async ({ page }) => {
    await open(page, "/backups", "version-mismatch");
    await openMenu(page, BILLING);
    await expect(item(page, "Open backup page")).not.toHaveAttribute("aria-disabled", "true");
    for (const name of [/^Recover artifact/, "Run recovery test", /^Scrub this backup/]) {
      await expect(item(page, name)).toHaveAttribute("aria-disabled", "true");
      await expect(item(page, name)).toContainText("This session is read-only");
    }
    await item(page, /^Recover artifact/).click();
    await expect(page.getByRole("dialog", { name: "Recover artifact" })).toHaveCount(0);
    await expect(menu(page)).toBeVisible();
  });

  test("#1202 a set with no recovery-test validator gets a disabled Run recovery test and the reason", async ({ page }) => {
    await open(page, "/backups", "assurance-no-validator", { wire: true });
    await openMenu(page, "redis-20260812.rdb.zst");
    const t = item(page, "Run recovery test");
    await expect(t).toHaveAttribute("aria-disabled", "true");
    await expect(t).toContainText(/no recovery-test validator is declared/i);
    // The reason is the item's description, so a screen reader hears it.
    const described = await t.getAttribute("aria-describedby");
    expect(described).toBeTruthy();
    await expect(page.locator("#" + described)).toContainText(/no recovery-test validator/i);
  });

  test("#1202 a server with no scrub route gets a disabled Scrub item that names the terminal command", async ({ page }) => {
    await open(page, "/backups", "assurance-no-operations", { wire: true });
    await openMenu(page, BILLING);
    const s = item(page, /^Scrub this backup/);
    await expect(s).toHaveAttribute("aria-disabled", "true");
    await expect(s).toContainText("retnd scrub production/billing-mysql");
    await expect(item(page, "Run recovery test"), "the test route is still offered").not.toHaveAttribute("aria-disabled", "true");
  });

  test("#1202 Retry ingestion is offered only on a FAILED backup, and says what the service answered", async ({ page }) => {
    await open(page, "/backups", "backups-ingest-failed");
    await openMenu(page, BILLING);
    await expect(item(page, "Retry ingestion")).toHaveCount(0);
    await page.keyboard.press("Escape");
    await openMenu(page, "media-week34.tar");
    await expect(menu(page).getByRole("group", { name: "Repair" })).toBeVisible();
    await item(page, "Retry ingestion").focus();
    await expect(menu(page).getByTestId("menu-echo")).toContainText("retnd retry media/weekly-archive/media-week34.tar");
    await item(page, "Retry ingestion").click();
    await expect(runs(page).getByRole("status")).toContainText(/Re-attempted: the service accepted the request/);
    await expect(page).toHaveURL(/\/backups\?/);
  });

  test("#1202 the menu leaves the selection alone and never opens the backup", async ({ page }) => {
    await open(page, "/backups", "backups-many");
    const boxes = page.getByRole("checkbox", { name: /^Select (?!all matching)/ });
    await boxes.nth(0).check();
    await boxes.nth(1).check();
    await expect(page.getByTestId("selection-count")).toContainText("2");
    const first = (await boxes.nth(0).getAttribute("aria-label"))!.replace(/^Select /, "");
    const third = (await boxes.nth(2).getAttribute("aria-label"))!.replace(/^Select /, "");
    await openMenu(page, first);
    await page.keyboard.press("Escape");
    await openMenu(page, third);
    await page.getByRole("heading", { name: "Backups" }).click();
    await expect(menu(page)).toHaveCount(0);
    await expect(page.getByTestId("selection-count")).toContainText("2");
    await expect(boxes.nth(0)).toBeChecked();
    await expect(boxes.nth(2)).not.toBeChecked();
    await expect(page).toHaveURL(/\/backups\?/);
  });

  for (const theme of ["light", "dark"] as const) {
    test("#1202 the Actions button, the menu and a disabled reason meet AA in the " + theme + " theme", async ({ page }) => {
      await open(page, "/backups", "assurance-no-operations", { wire: true });
      await setTheme(page, theme);
      await expect(actions(page, BILLING)).toBeVisible();
      expect(await measureContrast(page, "1202 actions button " + theme, '[data-row-actions]')).toEqual([]);
      expect(await measureEdges(page, "1202 actions button " + theme, '[data-row-actions] > button')).toEqual([]);
      await openMenu(page, BILLING);
      await expect(item(page, /^Scrub this backup/)).toHaveAttribute("aria-disabled", "true");
      // The first item has focus, so its focused fill is measured too.
      await expect(item(page, /^Recover artifact/)).toBeFocused();
      expect(await measureContrast(page, "1202 menu " + theme, '[role="menu"]')).toEqual([]);
      expect(await measureEdges(page, "1202 menu " + theme, '[role="menu"]')).toEqual([]);
    });

    test("#1202 the Backups table's column headers meet AA in the " + theme + " theme", async ({ page }) => {
      await open(page, "/backups");
      await expect(actions(page, BILLING)).toBeVisible();
      await setTheme(page, theme);
      expect(await measureContrast(page, "1202 table headers " + theme, "table.table thead")).toEqual([]);
    });

    test("#1202 a run started from the menu is readable in the " + theme + " theme", async ({ page }) => {
      test.setTimeout(60_000);
      await open(page, "/backups", "default", { wire: true });
      await openMenu(page, BILLING);
      await item(page, /^Scrub this backup/).click();
      await expect(runs(page).getByTestId("operation-progress")).toHaveAttribute("data-state", "complete", { timeout: 30_000 });
      await setTheme(page, theme);
      expect(await measureContrast(page, "1202 runs " + theme, '[aria-label="Started from this list"]')).toEqual([]);
    });
  }
});
