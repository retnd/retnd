/**
 * Slow pass over the Backup discovery card for the #930 GIFs. Run with
 * E2E_VIDEO=1. W930=before records main: it ends each stop on an assertion
 * that the Exclude row is still there, so the recording cannot be of a fixed
 * tree.
 */
import { expect, test } from "@playwright/test";
import { open, setTheme } from "./helpers";

const before = process.env.W930 === "before";
test.use({ viewport: { width: 1280, height: 900 }, video: process.env.E2E_VIDEO ? { mode: "on", size: { width: 1280, height: 900 } } : "off" });

test("930 walkthrough: Backup discovery card on a set with exclusions in the mock", async ({ page }) => {
  test.setTimeout(120_000);
  await open(page, "/sets/production/postgres-primary");
  const include = page.getByText("Include", { exact: true }).first();
  await expect(include).toBeVisible();
  await setTheme(page, "light");
  await include.evaluate((el) => el.scrollIntoView({ block: "center" }));
  await page.mouse.move(1270, 5);
  if (before) await expect(page.getByText("Exclude", { exact: true })).toHaveCount(1);
  else await expect(page.getByText("Exclude", { exact: true })).toHaveCount(0);
  await page.waitForTimeout(3000);
  await setTheme(page, "dark");
  if (before) await expect(page.getByText("Exclude", { exact: true })).toHaveCount(1);
  else await expect(page.getByText("Exclude", { exact: true })).toHaveCount(0);
  await page.waitForTimeout(3000);
});
