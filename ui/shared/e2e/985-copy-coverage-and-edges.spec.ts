import { expect, test } from "@playwright/test";
import { measureEdges, open, row, setTheme } from "./helpers";

// Issue #985, D3 and D8. The Content chip takes the strongest class across a
// backup's copies, so it used to read a bare "Content verified" for a backup
// whose other copies had only been seen to exist. It now says how many copies
// the claim covers and what the weakest of the rest is. And the Not configured
// chip's edge was 2.93:1 against its own fill; it is a UI boundary and needs 3.
const PG = "postgres-prod-20260828.dump.zst";
const content = (page: import("@playwright/test").Page, file: string) => row(page, file).locator('[data-claim="content"]');

for (const wire of [false, true]) {
  test.describe(`#985 D3 Content chip across copies (${wire ? "production seam" : "mock seam"})`, () => {
    test("one content-verified copy and one existence-only copy is not a bare 'Content verified'", async ({ page }) => {
      await open(page, "/backups", "default", { wire });
      const chip = content(page, PG);
      await expect(chip).toContainText("Content verified · 1 of 2 copies, 1 existence only");
      await expect(chip).toHaveAttribute("data-coverage", "partial");
      const text = (await chip.locator("[data-chip]").textContent()) ?? "";
      expect(text).not.toMatch(/Content verified(?!\s*·)/);
    });

    test("the artifact page says the same", async ({ page }) => {
      await open(page, "/backups", "default", { wire });
      await row(page, PG).click();
      await expect(page.locator('main [data-claim="content"]')).toContainText("1 of 2 copies, 1 existence only");
    });
  });
}

for (const theme of ["light", "dark"] as const) {
  test(`#985 D8 every chip edge is 3:1 against the page, and Not configured also against its fill (${theme})`, async ({ page }) => {
    for (const scenario of ["default", "assurance-more"]) {
      await open(page, "/backups", scenario);
      await expect(page.locator("[data-claim]").first()).toBeVisible();
      await setTheme(page, theme);
      expect(await measureEdges(page, `chips ${scenario} ${theme}`, '[data-testid="assurance-claims"] .achip', ["never_configured", "no_copy"])).toEqual([]);
    }
  });

  test(`#985 D8 the Not configured chip exists to be measured (${theme})`, async ({ page }) => {
    await open(page, "/backups");
    await expect(row(page, "media-week34.tar").locator('[data-claim="recovery"][data-state="never_configured"]')).toBeVisible();
    await setTheme(page, theme);
    await expect(row(page, "media-week34.tar").locator('[data-claim="stored"][data-state="no_copy"]')).toBeVisible();
  });
}
