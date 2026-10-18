/**
 * Slow passes for the #1175, #1180 and #1188 GIFs, one per theme. Run with E2E_VIDEO=1.
 *
 * W1175=before records the behaviour on main: each stop ends on an assertion
 * that the old behaviour is still there, so a recording cannot be of a fixed
 * tree by accident.
 */
import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { open, preselectTheme, setTheme, spaGo } from "./helpers";

const S = "assurance-default-untested";
const SET = "/sets/production/postgres-primary";
const NEWEST = "postgres-prod-20260829.dump.zst";
const NEWEST_PATH = "/backups/production/postgres-primary/" + NEWEST;
const before = process.env.W1175 === "before";
const beat = async (p: Page, ms = 1100) => {
  await p.mouse.move(1270, 5);
  await p.waitForTimeout(ms);
};
const into = (loc: Locator) => loc.evaluate((el) => el.scrollIntoView({ block: "center" }));
const step = (page: Page, label: string) => page.getByRole("listitem").filter({ hasText: new RegExp("^" + label) });
const listRow = (page: Page, file: string) => page.getByRole("row").filter({ has: page.locator("td.mono", { hasText: file }) });

test.use({ viewport: { width: 1280, height: 900 }, video: process.env.E2E_VIDEO ? { mode: "on", size: { width: 1280, height: 900 } } : "off" });

for (const theme of ["light", "dark"] as const) {
  test(`1175 walkthrough (${theme}): the chip and the Lifecycle agree, then Custom configuration and its link`, async ({ page }) => {
    test.setTimeout(180_000);
    await preselectTheme(page, theme);
    await open(page, NEWEST_PATH, S, { wire: true });
    const card = page.getByRole("region", { name: "Assurance" });
    await expect(card.locator('[data-claim="content"]')).toContainText("Content verified");
    await setTheme(page, theme);
    const verified = step(page, "VERIFIED");
    await into(verified);
    if (before) await expect(verified).toContainText("\u2014");
    else await expect(verified).toContainText(/\d\d:\d\d:\d\d/);
    await beat(page, 3000);

    await spaGo(page, SET);
    await setTheme(page, theme);
    const section = page.getByRole("region", { name: "Assurance" });
    await section.getByRole("radio", { name: "This set's own policy" }).check();
    await section.getByRole("button", { name: "Save policy" }).click();
    await expect(section.getByText("Saved")).toBeVisible();
    await beat(page, 1500);

    await spaGo(page, "/backups");
    await setTheme(page, theme);
    const chip = listRow(page, NEWEST).locator('[data-claim="recovery"]');
    await into(chip);
    if (before) await expect(chip).toContainText("This set's own policy");
    else await expect(chip).toContainText("Custom configuration");
    await beat(page, 2500);

    if (!before) {
      await chip.getByRole("link", { name: "Custom configuration" }).click();
      await expect(page).toHaveURL(/#assurance$/);
      await expect(page.locator("#assurance")).toBeInViewport();
      await beat(page, 2500);
    }
  });

  test(`1188 walkthrough (${theme}): the Default configuration chip lands on the Assurance section`, async ({ page }) => {
    test.setTimeout(120_000);
    await preselectTheme(page, theme);
    await open(page, "/backups", S, { wire: true });
    await expect(page.locator("main")).toBeVisible();
    await setTheme(page, theme);
    const chip = listRow(page, NEWEST).locator('[data-claim="recovery"]').getByRole("link", { name: "Default configuration" });
    await into(chip);
    await beat(page, 2000);
    await chip.click();
    await page.waitForTimeout(2800);
    const heading = page.locator(before ? "#assurance-defaults" : "#assurance").getByRole("heading", { name: "Assurance" });
    const y = (await heading.boundingBox())!.y;
    if (before) expect(y).toBeGreaterThan(300);
    else { await expect(heading).toBeInViewport({ ratio: 1 }); expect(y).toBeLessThan(160); }
    await beat(page, 3000);
  });

  test(`1180 walkthrough (${theme}): page size, paging, a remembered choice, All, select all across pages`, async ({ page }) => {
    test.setTimeout(180_000);
    const size = page.getByRole("combobox", { name: "Rows per page" });
    await preselectTheme(page, theme);
    await open(page, "/backups", "backups-many");
    await expect(page.locator("main")).toBeVisible();
    await setTheme(page, theme);
    if (before) {
      await expect(size).toHaveCount(0);
      await expect(page.getByRole("checkbox", { name: /^Select (?!all matching)/ })).toHaveCount(34);
      await into(page.getByTestId("backups-footer"));
      await beat(page, 3500);
      return;
    }
    await expect(size).toHaveValue("20");
    await into(page.getByTestId("backups-paging"));
    await beat(page, 2000);
    await page.getByRole("button", { name: "Next page" }).click();
    await expect(page.getByTestId("backups-page-status")).toContainText("Page 2 of 2");
    await beat(page, 1800);
    await page.getByRole("button", { name: "Previous page" }).click();
    await size.selectOption("50");
    await beat(page, 1500);
    await page.reload();
    await expect(size).toHaveValue("50");
    await setTheme(page, theme);
    await into(page.getByTestId("backups-paging"));
    await beat(page, 2200);
    await size.selectOption("All");
    await expect(page.getByTestId("backups-page-status")).toContainText("34 of 34");
    await beat(page, 1500);
    await page.reload();
    await expect(size).toHaveValue("50");
    await setTheme(page, theme);
    await size.selectOption("20");
    await page.getByRole("checkbox", { name: "Select all matching backups" }).check();
    await expect(page.getByTestId("selection-count")).toContainText("33 selected");
    await beat(page, 1500);
    await page.getByRole("button", { name: "Next page" }).click();
    await expect(page.getByTestId("selection-count")).toContainText("33 selected");
    await into(page.getByTestId("backups-paging"));
    await beat(page, 3000);
  });
}
