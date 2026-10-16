/**
 * Issue #1179, from the review of #1185: the folder browser behind "Under
 * the staging root on this retnd host" (#1190's shared field).
 *
 *   - A folder removed between two clicks is a 404 from the host
 *     (DIRECTORY_NOT_FOUND), and the browser steps back up to the folder above
 *     it and says so, instead of reading as "the host cannot list its folders".
 *   - A listing refused because the session ended takes the app to sign-in,
 *     the way every other read does, instead of blaming the host.
 *   - "Use this folder" says the name it will set.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, row } from "./helpers";

const PG = "postgres-prod-20260828.dump.zst";
const dialog = (p: Page) => p.getByRole("dialog", { name: "Recover artifact" });
const browserOf = (p: Page) => dialog(p).getByRole("group", { name: "Folders on the host" });

async function stagingBrowser(page: Page, scenario = "default") {
  await open(page, "/backups", scenario);
  await row(page, PG).click();
  await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
  await expect(dialog(page)).toBeVisible();
  await dialog(page).getByRole("radio", { name: "Under the staging root on this retnd host" }).check();
  await expect(browserOf(page)).toBeVisible();
  return browserOf(page);
}

test.describe("#1179 the folder browser after the review", () => {
  test("a folder that is gone by the time it is opened steps back up and says so", async ({ page }) => {
    const browser = await stagingBrowser(page, "host-browse-vanished");
    await browser.getByRole("button", { name: "Open folder archive-2025" }).click();
    await expect(browser.getByTestId("browser-gone")).toContainText("archive-2025");
    await expect(browser.getByTestId("browser-path")).toHaveText("(the root)");
    // the root's own folders are listed again, and nothing blames the host
    await expect(browser.getByRole("button", { name: "Open folder daily" })).toBeVisible();
    await expect(browser.getByRole("alert")).toHaveCount(0);
    // and the next move clears the note
    await browser.getByRole("button", { name: "Open folder daily" }).click();
    await expect(browser.getByTestId("browser-path")).toHaveText("daily");
    await expect(browser.getByTestId("browser-gone")).toHaveCount(0);
  });

  test("a listing refused because the session ended goes to sign-in", async ({ page }) => {
    await open(page, "/backups", "host-browse-session-ended");
    await row(page, PG).click();
    await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
    await expect(dialog(page)).toBeVisible();
    // From here on the service says this browser has no session, which is
    // what a restarted engine answers.
    await page.route("**/api/v1/auth/session", (r) => r.fulfill({ status: 401, json: { error: { code: "UNAUTHENTICATED", message: "authentication required" } } }));
    await dialog(page).getByRole("radio", { name: "Under the staging root on this retnd host" }).check();
    await expect(page.getByRole("button", { name: /sign in/i })).toBeVisible({ timeout: 10_000 });
  });

  test("Use this folder says the name it sets", async ({ page }) => {
    const browser = await stagingBrowser(page);
    await browser.getByRole("button", { name: "Open folder weekly" }).click();
    await expect(browser.getByTestId("browser-pick")).toHaveText("weekly/" + PG);
    const use = browser.getByRole("button", { name: "Use this folder" });
    await expect(use).toHaveAccessibleDescription("Sets the name to weekly/" + PG);
    await use.click();
    await expect(dialog(page).getByRole("textbox", { name: "Name under the staging root" })).toHaveValue("weekly/" + PG);
  });
});
