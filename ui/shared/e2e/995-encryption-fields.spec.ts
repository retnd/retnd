/**
 * Issue #995 follow-up: Backups list column, digest rows, key counts. WRITTEN,
 * NOT RUN. When run, only through /Users/rom/workspace/xdg-pw-lock.sh.
 */
import { expect, test } from "@playwright/test";
import { measureContrast, open, row, setTheme } from "./helpers";

const PG = "postgres-prod-20260828.dump.zst";

test.describe("#995 follow-up", () => {
  test("the Backups list has an Encryption column only when a copy is encrypted", async ({ page }) => {
    await open(page, "/backups", "default");
    await expect(page.getByRole("columnheader", { name: /Encryption/ })).toHaveCount(0);
    await open(page, "/backups", "encryption-on");
    await expect(page.getByRole("columnheader", { name: /Encryption/ })).toBeVisible();
    const r = row(page, PG);
    await expect(r.locator('[data-list-copy="local"]').getByRole("group", { name: "Plaintext" })).toBeVisible();
    await expect(r.locator('[data-list-copy="offsite_s3"]').getByRole("group", { name: /^Encrypted/ })).toBeVisible();
    await expect(r.getByRole("group", { name: /Verified recoverable/ })).toHaveCount(0);
  });

  test("a copy with a missing key says so on the list row", async ({ page }) => {
    await open(page, "/backups", "encryption-key-missing");
    await expect(row(page, PG).getByRole("group", { name: /^Key missing/ })).toBeVisible();
  });

  test("Backup detail names the plaintext digest and the stored-bytes digest as two rows", async ({ page }) => {
    await open(page, "/backups", "encryption-on");
    await row(page, PG).click();
    await expect(page.getByText("Plaintext digest")).toBeVisible();
    await expect(page.getByText(/Stored-bytes digest, offsite_s3/)).toBeVisible();
    await expect(page.getByText(/Checksum \(plaintext\)/)).toBeVisible();
  });

  test("a key's copy count links to the Backups list filtered to that key", async ({ page }) => {
    await open(page, "/settings", "encryption-in-use");
    await page.getByRole("link", { name: /41 in Backups/ }).click();
    await expect(page).toHaveURL(/\/backups\?.*key=mk-2026-11-09-a/);
    await expect(page.getByText(/Copies under key mk-2026-11-09-a/)).toBeVisible();
  });

  test("exported, not yet confirmed survives a reload because the key says it", async ({ page }) => {
    await open(page, "/settings", "encryption-exported");
    await expect(page.getByText("Bundle exported, not yet confirmed")).toBeVisible();
    await page.reload();
    await expect(page.getByText("Bundle exported, not yet confirmed")).toBeVisible();
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the list column meets WCAG AA in ${theme}`, async ({ page }) => {
      await open(page, "/backups", "encryption-key-missing");
      await setTheme(page, theme);
      expect(await measureContrast(page, "995 list column " + theme, "[data-list-copy]"), theme).toEqual([]);
    });
  }
});
