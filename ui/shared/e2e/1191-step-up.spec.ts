/**
 * Issue #1191 after #1167's review: a recovery onto the source host takes the
 * same password check a restore does, so it answers the same way. A tripped
 * limiter says wait rather than "check your password", a wrong password says
 * nothing was written, and on a connection that is not encrypted the
 * operator is asked BEFORE the password leaves the browser.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, row, setTheme } from "./helpers";
import { ACK, SOURCE } from "./1191-source-host";

const PG = "postgres-prod-20260828.dump.zst";
const PASSWORD = "correct-horse-battery";
const dialog = (p: Page) => p.getByRole("dialog", { name: "Recover artifact" });
const go = (p: Page) => dialog(p).getByRole("button", { name: "Recover artifact" });

async function sourceWithPassword(page: Page, scenario: string, password = PASSWORD) {
  await open(page, "/backups", scenario);
  await row(page, PG).click();
  await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
  const dlg = dialog(page);
  await expect(dlg.getByRole("radio", { name: SOURCE })).toBeChecked();
  await dlg.getByRole("checkbox", { name: ACK }).check();
  await dlg.getByLabel("Your password").fill(password);
  await go(page).click();
  return dlg;
}

test.describe("#1191 the source host's password check answers like a restore's", () => {
  test("a tripped limiter says wait, not check the password", async ({ page }) => {
    const dlg = await sourceWithPassword(page, "step-up-limited");
    const alert = dlg.locator("[data-source-step-up-error]");
    await expect(alert).toBeVisible();
    await expect(alert).toContainText(/wait a minute/i);
    await expect(alert).toContainText("Nothing was written to the source host");
    await expect(alert).not.toContainText(/check it/i);
    // Back on the choice, still on the source host, so the operator can try later.
    await expect(dlg.getByRole("radio", { name: SOURCE })).toBeChecked();
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      expect(await measureContrast(page, "1191 rate limited " + theme, "[data-source-step-up-error]"), theme).toEqual([]);
    }
  });

  test("control: a wrong password is a wrong password", async ({ page }) => {
    const dlg = await sourceWithPassword(page, "local-account-profile", "not-the-password");
    const alert = dlg.locator("[data-source-step-up-error]");
    await expect(alert).toContainText("That password was not accepted");
    await expect(alert).not.toContainText(/wait a minute/i);
  });

  test("on an unencrypted connection the question comes before the password is sent", async ({ page }) => {
    const dlg = await sourceWithPassword(page, "insecure-transport");
    const ask = page.getByRole("dialog", { name: "This connection is not encrypted" });
    await expect(ask).toBeVisible();
    await ask.getByRole("button", { name: "Cancel" }).click();
    const alert = dlg.locator("[data-source-step-up-error]");
    await expect(alert).toContainText(/nothing was sent/i);
    await expect(dlg.getByRole("radio", { name: SOURCE })).toBeChecked();
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      expect(await measureContrast(page, "1191 nothing sent " + theme, "[data-source-step-up-error]"), theme).toEqual([]);
    }

    // A yes lets the same press through.
    await go(page).click();
    await page.getByRole("dialog", { name: "This connection is not encrypted" }).getByRole("button", { name: "Continue anyway" }).click();
    await expect(dlg.locator("[data-source-step-up-error]")).toHaveCount(0);
    await expect(dlg).toHaveAttribute("data-phase", /minting|verifying|placed/);
  });
});
