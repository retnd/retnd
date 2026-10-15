/**
 * A slow, watchable pass over the three screens #1190 is about, for the GIFs
 * in the issue and the PR. Run with E2E_VIDEO=1.
 *
 * The walk finds each screen's controls loosely (any radio about staging, any
 * about a destination), so it gets through the screens however they spell the
 * two choices and the recording shows what each one actually offers. It ends
 * on the strict assertion: all three show the same words, the same folder
 * field and the same drop-down.
 */
import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { open, row } from "./helpers";
import { DESTINATION, STAGING, destinationField, folderField } from "./1190-pickers";

const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);
const anyStaging = (scope: Locator) => scope.getByRole("radio", { name: /staging/i });
const anyDestination = (scope: Locator) => scope.getByRole("radio", { name: /destination/i });

/** What each screen offered, read off the page as the walk passes it. */
async function offered(scope: Locator) {
  const radios = await scope.getByRole("radio").evaluateAll((rs) => rs.map((r) => (r.closest("label")?.textContent ?? "").trim()));
  return {
    radios: radios.filter((t) => /staging|destination/i.test(t)),
    folderField: await folderField(scope).count(),
    destinationField: await destinationField(scope).count()
  };
}

async function walk(page: Page, scope: Locator) {
  await anyStaging(scope).check();
  await beat(page, 1400);
  const browser = scope.getByRole("group", { name: "Folders on the host" });
  await browser.getByRole("button", { name: "Open folder daily" }).click();
  await beat(page, 900);
  await browser.getByRole("button", { name: "Use this folder" }).click();
  await beat(page, 1200);
  const seen = await offered(scope);
  await anyDestination(scope).check();
  await beat(page, 1400);
  return { ...seen, destinationField: await destinationField(scope).count() };
}

test("#1190 walkthrough: the staging folder and the destination are the same on every screen", async ({ page }) => {
  // 1. a backup's page, Recover artifact
  await open(page, "/backups");
  await beat(page);
  await row(page, "postgres-prod-20260828.dump.zst").click();
  await beat(page, 600);
  await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
  const one = page.getByRole("dialog", { name: "Recover artifact" });
  await expect(one).toBeVisible();
  await beat(page);
  const artifact = await walk(page, one);
  await page.keyboard.press("Escape");

  // 2. the Backups list, a selection
  await open(page, "/backups", "backups-many");
  await beat(page);
  const boxes = page.getByRole("checkbox", { name: /^Select (?!all)/ });
  await boxes.nth(0).check();
  await boxes.nth(1).check();
  const bar = page.getByRole("region", { name: "Selected backups" });
  await expect(bar).toBeVisible();
  await beat(page);
  const selection = await walk(page, bar);

  // 3. a set's page, Recover this backup set
  await open(page, "/sets/production/auth-config");
  await beat(page);
  await page.getByRole("button", { name: "Recover this backup set", exact: true }).click();
  const set = page.getByRole("dialog", { name: "Recover this backup set" });
  await expect(set).toBeVisible();
  await beat(page);
  const wholeSet = await walk(page, set);
  await beat(page, 1500);

  const same = { radios: [STAGING, DESTINATION], folderField: 1, destinationField: 1 };
  expect({ artifact, selection, wholeSet }).toEqual({ artifact: same, selection: same, wholeSet: same });
});
