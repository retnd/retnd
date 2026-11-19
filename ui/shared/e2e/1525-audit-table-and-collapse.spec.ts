/**
 * Issue #1525: the agent Audit list on Settings stacks every call vertically
 * (a container query turns the table into cards below 640px) and is always
 * open, so a handful of calls fill the page.
 *
 * What I want: it stays a table at every width, scrolling sideways inside its
 * own box when the columns do not fit, and the whole Audit region is
 * collapsible and collapsed by default, with a header that says how many calls
 * the filter returns.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { openAgents, region } from "./1351-agents";

const NARROW = { width: 760, height: 900 };
const toggle = (p: Page) => region(p, "audit").getByRole("button", { name: /^Audit/ });
const rows = (p: Page) => region(p, "audit").locator("tbody tr[data-audit-row]");

async function narrow(page: Page) {
  await page.setViewportSize(NARROW);
  await openAgents(page);
}

test.describe("#1525 the agent audit is a table, and a collapsed one", () => {
  test("#1525 it starts collapsed, and the header says how many calls are behind it", async ({ page }) => {
    await narrow(page);
    await expect(toggle(page)).toHaveAttribute("aria-expanded", "false");
    await expect(toggle(page)).toContainText(/8/);
    await expect(rows(page).first()).toBeHidden();
  });

  test("#1525 pressing the header opens it and pressing it again closes it", async ({ page }) => {
    await narrow(page);
    await toggle(page).click();
    await expect(toggle(page)).toHaveAttribute("aria-expanded", "true");
    await expect(rows(page)).toHaveCount(8);
    await expect(rows(page).first()).toBeVisible();
    await toggle(page).click();
    await expect(toggle(page)).toHaveAttribute("aria-expanded", "false");
    await expect(rows(page).first()).toBeHidden();
  });

  test("#1525 at a narrow width a call is one row: its cells share one line", async ({ page }) => {
    await narrow(page);
    await toggle(page).click();
    const row = rows(page).first();
    const display = await row.evaluate((el) => getComputedStyle(el).display);
    expect(display).toBe("table-row");
    const time = (await row.locator("td").nth(0).boundingBox())!;
    const corr = (await row.locator("td").nth(5).boundingBox())!;
    expect(Math.abs(time.y - corr.y)).toBeLessThan(4);
    expect(time.height).toBeLessThan(120);
  });

  test("#1525 the column headers stay visible, so the columns are labelled once", async ({ page }) => {
    await narrow(page);
    await toggle(page).click();
    for (const h of ["Time", "Token", "Tool", "Needs", "Outcome", "Correlation id"]) {
      await expect(region(page, "audit").locator("thead th", { hasText: h })).toBeVisible();
    }
  });

  test("#1525 the table scrolls inside its own box and the page does not scroll sideways", async ({ page }) => {
    await narrow(page);
    await toggle(page).click();
    const overflow = await page.evaluate(() => document.scrollingElement!.scrollWidth - window.innerWidth);
    expect(overflow).toBeLessThanOrEqual(1);
  });

  test("#1525 the filters and the terminal echo are inside the collapsed region", async ({ page }) => {
    await narrow(page);
    await expect(region(page, "audit").getByLabel("Since")).toBeHidden();
    await toggle(page).click();
    await expect(region(page, "audit").getByLabel("Since")).toBeVisible();
    await expect(region(page, "audit")).toContainText("retnd agent audit");
  });
});
