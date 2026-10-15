import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1180: the Backups page pages its list by 20, 50 or 100. "All" is
 * offered only while the deployment holds fewer than 1000 backups. A chosen
 * 20, 50 or 100 is remembered between sessions (localStorage, and the page works
 * without it); "All" is never remembered. Selection and the select-all after a
 * filter from #1107 keep working across pages.
 *
 * backups-many lists 34 backups (33 selectable). backups-999 and backups-1000
 * are deployments holding exactly that many.
 */
const MANY = "backups-many";
/** A row checkbox: "Select <file>", never the header's "Select all matching backups". */
const ROW = /^Select (?!all matching)/;
const size = (page: Page) => page.getByRole("combobox", { name: "Rows per page" });
const next = (page: Page) => page.getByRole("button", { name: "Next page" });
const prev = (page: Page) => page.getByRole("button", { name: "Previous page" });
const rows = (page: Page) => page.getByRole("checkbox", { name: ROW });
const where = (page: Page) => page.getByTestId("backups-page-status");
const bar = (page: Page) => page.getByRole("region", { name: "Selected backups" });
const count = (page: Page) => page.getByTestId("selection-count");
const headerBox = (page: Page) => page.getByRole("checkbox", { name: "Select all matching backups" });
const options = async (page: Page) => {
  await expect(size(page)).toBeVisible();
  return (await size(page).locator("option").allTextContents()).map((t) => t.trim());
};

test.describe("#1180 page size", () => {
  test("#1180 the page offers 20, 50, 100 and All, and starts at 20 rows", async ({ page }) => {
    await open(page, "/backups", MANY);
    await expect(size(page)).toBeVisible();
    expect(await options(page)).toEqual(["20", "50", "100", "All"]);
    await expect(size(page)).toHaveValue("20");
    await expect(rows(page)).toHaveCount(20);
    await expect(where(page)).toContainText("Page 1 of 2");
    await expect(where(page)).toContainText("1–20 of 34");
  });

  test("#1180 a bigger page shows more rows, and a page that holds them all has nowhere to go", async ({ page }) => {
    await open(page, "/backups", MANY);
    await size(page).selectOption("50");
    await expect(rows(page)).toHaveCount(34);
    await expect(where(page)).toContainText("Page 1 of 1");
    await expect(next(page)).toBeDisabled();
    await expect(prev(page)).toBeDisabled();
  });

  test("#1180 Next and Previous move between pages, and the ends are disabled", async ({ page }) => {
    await open(page, "/backups", MANY);
    await expect(prev(page)).toBeDisabled();
    await next(page).click();
    await expect(rows(page)).toHaveCount(14);
    await expect(where(page)).toContainText("Page 2 of 2");
    await expect(where(page)).toContainText("21–34 of 34");
    await expect(next(page)).toBeDisabled();
    await prev(page).click();
    await expect(rows(page)).toHaveCount(20);
    await expect(where(page)).toContainText("Page 1 of 2");
  });

  test("#1180 All shows every backup on one page", async ({ page }) => {
    await open(page, "/backups", MANY);
    await size(page).selectOption("All");
    await expect(rows(page)).toHaveCount(34);
    await expect(where(page)).toContainText("34 of 34");
    await expect(next(page)).toBeDisabled();
  });

  test("#1180 the page is a real control for a keyboard: the size is a labelled select", async ({ page }) => {
    await open(page, "/backups", MANY);
    await size(page).focus();
    await expect(size(page)).toBeFocused();
    await page.keyboard.press("ArrowDown");
    await expect(size(page)).toHaveValue("50");
  });
});

test.describe("#1180 All is offered only under 1000 backups", () => {
  test("#1180 999 backups offer All, and All shows all 999", async ({ page }) => {
    await open(page, "/backups", "backups-999");
    expect(await options(page)).toEqual(["20", "50", "100", "All"]);
    await size(page).selectOption("All");
    await expect(rows(page)).toHaveCount(999);
  });

  test("#1180 1000 backups do not offer All", async ({ page }) => {
    await open(page, "/backups", "backups-1000");
    expect(await options(page)).toEqual(["20", "50", "100"]);
    await expect(rows(page)).toHaveCount(20);
    await expect(where(page)).toContainText("Page 1 of 50");
  });
});

test.describe("#1180 the choice is remembered", () => {
  test("#1180 a chosen 50 is still 50 after the page is reloaded", async ({ page }) => {
    await open(page, "/backups", MANY);
    await size(page).selectOption("50");
    await page.reload();
    await expect(size(page)).toHaveValue("50");
    await expect(rows(page)).toHaveCount(34);
  });

  test("#1180 a chosen 100 is remembered too", async ({ page }) => {
    await open(page, "/backups", MANY);
    await size(page).selectOption("100");
    await page.reload();
    await expect(size(page)).toHaveValue("100");
  });

  test("#1180 All is not remembered: after a reload the last 20, 50 or 100 comes back", async ({ page }) => {
    await open(page, "/backups", MANY);
    await size(page).selectOption("50");
    await size(page).selectOption("All");
    await expect(rows(page)).toHaveCount(34);
    await page.reload();
    await expect(size(page)).toHaveValue("50");
    await expect(size(page)).not.toHaveValue("All");
  });

  test("#1180 All chosen on a fresh browser leaves the default of 20 for the next visit", async ({ page }) => {
    await open(page, "/backups", MANY);
    await size(page).selectOption("All");
    await page.reload();
    await expect(size(page)).toHaveValue("20");
  });

  test("#1180 a remembered size that is not 20, 50 or 100 is ignored", async ({ page }) => {
    await open(page, "/backups", MANY);
    await size(page).selectOption("50");
    await page.evaluate(() => {
      for (const k of Object.keys(localStorage)) if (localStorage.getItem(k) === "50") localStorage.setItem(k, "7");
    });
    await page.reload();
    await expect(size(page)).toHaveValue("20");
  });

  test("#1180 with storage unavailable the page still works, and keeps the choice for the visit", async ({ page }) => {
    await page.addInitScript(() => {
      // Only this page's key is refused: the rest of the app reads storage
      // unguarded and falls over when ALL of it is blocked, which is not what
      // this test is about.
      const get = Storage.prototype.getItem;
      const set = Storage.prototype.setItem;
      const refuse = () => { throw new DOMException("storage is blocked", "SecurityError"); };
      Storage.prototype.getItem = function (k: string) { return k === "retnd.backups.pageSize" ? refuse() : get.call(this, k); };
      Storage.prototype.setItem = function (k: string, v: string) { return k === "retnd.backups.pageSize" ? refuse() : set.call(this, k, v); };
    });
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await open(page, "/backups", MANY);
    await expect(rows(page)).toHaveCount(20);
    await size(page).selectOption("50");
    await expect(rows(page)).toHaveCount(34);
    await expect(next(page)).toBeDisabled();
    expect(errors).toEqual([]);
  });
});

test.describe("#1180 selection and filters across pages", () => {
  test("#1180 select all matching takes every page, and the other page shows its rows ticked", async ({ page }) => {
    await open(page, "/backups", MANY);
    await headerBox(page).check();
    await expect(count(page)).toContainText("33 selected");
    await expect(count(page)).toContainText("of 33 matching");
    await next(page).click();
    await expect(rows(page)).toHaveCount(14);
    for (const n of await rows(page).all()) if (await n.isEnabled()) await expect(n).toBeChecked();
    await expect(count(page)).toContainText("33 selected");
  });

  test("#1180 a row ticked on one page stays ticked after paging away and back", async ({ page }) => {
    await open(page, "/backups", MANY);
    const first = page.getByRole("checkbox", { name: "Select postgres-primary-20260801.bak", exact: true });
    await first.check();
    await next(page).click();
    await expect(count(page)).toContainText("1 selected");
    await prev(page).click();
    await expect(first).toBeChecked();
  });

  test("#1180 'Select all N matching' from the bar reaches rows on pages not shown", async ({ page }) => {
    await open(page, "/backups", MANY);
    await page.getByRole("checkbox", { name: "Select postgres-primary-20260801.bak", exact: true }).check();
    await bar(page).getByRole("button", { name: "Select all 33 matching" }).click();
    await expect(count(page)).toContainText("33 selected");
    await next(page).click();
    await expect(count(page)).toContainText("33 selected");
  });

  test("#1180 select all after a set filter still selects exactly that set's rows, and a filter returns to page 1", async ({ page }) => {
    await open(page, "/backups", MANY);
    await next(page).click();
    await expect(where(page)).toContainText("Page 2 of 2");
    await page.getByRole("combobox", { name: "Filter by backup set" }).selectOption({ label: "Billing MySQL" });
    await expect(where(page)).toContainText("Page 1 of 1");
    await expect(rows(page)).toHaveCount(11);
    await headerBox(page).check();
    await expect(count(page)).toContainText("11 selected");
    await expect(count(page)).toContainText("of 11 matching");
    const cmd = await bar(page).getByTestId("recover-command").innerText();
    expect(cmd.match(/production\/billing-mysql\//g)?.length).toBe(11);
  });

  test("#1180 changing a filter still clears a selection held across pages, and says so", async ({ page }) => {
    await open(page, "/backups", MANY);
    await headerBox(page).check();
    await next(page).click();
    await page.getByRole("combobox", { name: "Filter by backup set" }).selectOption({ label: "Auth service config" });
    await expect(bar(page)).toHaveCount(0);
    await expect(page.getByTestId("selection-notice")).toContainText(/selection was cleared because the filter changed/i);
  });

  test("#1180 the recovery command names every selected backup, not only the page on screen", async ({ page }) => {
    await open(page, "/backups", MANY);
    await headerBox(page).check();
    const cmd = await bar(page).getByTestId("recover-command").innerText();
    // the first backup is on page one, the last (day 30) on page two
    expect(cmd).toContain("postgres-primary-20260801.bak");
    expect(cmd).toContain("auth-config-20260830.bak");
  });
});

test.describe("#1180 contrast of the paging controls", () => {
  for (const theme of ["light", "dark"] as const) {
    test(`#1180 the paging row meets AA in the ${theme} theme`, async ({ page }) => {
      await open(page, "/backups", MANY);
      await expect(size(page)).toBeVisible();
      await setTheme(page, theme);
      expect(await measureContrast(page, `#1180 paging row, ${theme}`, '[data-testid="backups-paging"]')).toEqual([]);
    });
  }
});
