import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, measureContrast, setTheme } from "./helpers";

// Issue #1186: the sidebar's Backups badge counted backup SETS (it was summed
// from the health report), so a deployment holding 119 retained backups in 3
// sets read "3". It now counts the retained backups themselves: the exact
// number up to 100, and ">100" above it. ?artifacts=N is the mock's way of
// holding exactly N retained backups (api/mock.ts, padRetained).

const nav = (page: Page) => page.getByRole("navigation", { name: "Sections" });
const badge = (page: Page, label: string) => nav(page).getByRole("link", { name: new RegExp("^" + label) }).locator("span.mono");

test.describe("#1186 the Backups badge counts backups, not sets", () => {
  test("119 retained backups read >100, with a label that says so, and Backup sets still counts its 4 sets", async ({ page }) => {
    await open(page, "/?artifacts=119");
    await expect(badge(page, "Backups")).toHaveText(">100");
    await expect(badge(page, "Backups")).toHaveAttribute("aria-label", "more than 100 backups");
    await expect(nav(page).getByRole("link", { name: /^Backups.*more than 100 backups/ })).toBeVisible();
    await expect(badge(page, "Backup sets")).toHaveText("4");
  });

  test("40 retained backups read 40, labelled as a count", async ({ page }) => {
    await open(page, "/?artifacts=40");
    await expect(badge(page, "Backups")).toHaveText("40");
    await expect(badge(page, "Backups")).toHaveAttribute("aria-label", "40 backups");
    await expect(badge(page, "Backup sets")).toHaveText("4");
  });

  test("the cap is exact: 100 reads 100 and 101 reads >100", async ({ page }) => {
    await open(page, "/?artifacts=100");
    await expect(badge(page, "Backups")).toHaveText("100");
    await open(page, "/?artifacts=101");
    await expect(badge(page, "Backups")).toHaveText(">100");
  });

  for (const theme of ["light", "dark"] as const) {
    test("the capped badge text meets AA in the " + theme + " theme", async ({ page }) => {
      await open(page, "/?artifacts=119");
      await setTheme(page, theme);
      await expect(badge(page, "Backups")).toHaveText(">100");
      const failures = await measureContrast(page, "sidebar backups badge " + theme, 'nav[aria-label="Sections"] a[href="/backups"]');
      expect(failures).toEqual([]);
    });
  }

  for (const theme of ["light", "dark"] as const) {
    test("the badge text meets AA on the active row in the " + theme + " theme", async ({ page }) => {
      await open(page, "/backups?artifacts=119");
      await setTheme(page, theme);
      await expect(badge(page, "Backups")).toHaveText(">100");
      const failures = await measureContrast(page, "sidebar backups badge, active row " + theme, 'nav[aria-label="Sections"] a[href="/backups"]');
      expect(failures).toEqual([]);
    });
  }
});
