/**
 * Issue #1203: an upload is the only route that puts new code where the engine
 * will run it, so it takes what a recovery onto the source host takes: the
 * destructive gate (shut on a gateway profile, where a forged identity header
 * would otherwise be enough to plant a script) and the administrator's password
 * again. The password rides one request and is not kept; on a connection that is
 * not encrypted the operator is asked before it leaves the browser.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

const POSTGRES = String.raw`^postgres-prod-\d{8}\.dump\.zst$`;
const PASSWORD = "correct-horse-battery";
const card = (p: Page) => p.getByRole("region", { name: "Recovery-test validators" });
const dialog = (p: Page) => p.getByRole("dialog", { name: "Add validator" });
const upload = (p: Page) => dialog(p).getByRole("button", { name: "Upload validator" });

async function filled(page: Page, scenario: string) {
  await open(page, "/settings", scenario);
  await card(page).getByRole("button", { name: "Add validator" }).click();
  const dlg = dialog(page);
  await dlg.getByLabel("Name", { exact: true }).fill("postgres-dump");
  await dlg.getByRole("radio", { name: POSTGRES }).check();
  await dlg.getByRole("button", { name: "Start from the default script" }).click();
  return dlg;
}

test.describe("#1203 upload takes the password and the gate", () => {
  test("a profile that re-checks the password asks for it, and waits for it", async ({ page }) => {
    const dlg = await filled(page, "local-account-profile");
    await expect(dlg.getByLabel("Your password")).toBeVisible();
    await expect(dlg).toContainText("sent once with this request and not kept");
    await expect(upload(page)).toBeDisabled();
    await dlg.getByLabel("Your password").fill(PASSWORD);
    await expect(upload(page)).toBeEnabled();
    await upload(page).click();
    await expect(dialog(page)).toHaveCount(0);
    await expect(card(page).getByRole("row").filter({ hasText: "postgres-dump" })).toHaveCount(1);
  });

  test("a wrong password says nothing was uploaded and keeps the dialog", async ({ page }) => {
    const dlg = await filled(page, "local-account-profile");
    await dlg.getByLabel("Your password").fill("not-the-password");
    await upload(page).click();
    const alert = dlg.locator("[data-validator-error]");
    await expect(alert).toContainText("That password was not accepted");
    await expect(alert).toContainText("No validator was uploaded");
    await expect(dlg.getByLabel("Script", { exact: true })).not.toHaveValue("");
    await expect(card(page).getByRole("row").filter({ hasText: "postgres-dump" })).toHaveCount(0);
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      expect(await measureContrast(page, "1203 wrong password " + theme, "[data-validator-error]"), theme).toEqual([]);
    }
  });

  test("a tripped limiter says wait, not check the password", async ({ page }) => {
    const dlg = await filled(page, "step-up-limited");
    await dlg.getByLabel("Your password").fill(PASSWORD);
    await upload(page).click();
    const alert = dlg.locator("[data-validator-error]");
    await expect(alert).toContainText(/wait a minute/i);
    await expect(alert).not.toContainText(/check it/i);
  });

  test("on an unencrypted connection the question comes before the password is sent", async ({ page }) => {
    const dlg = await filled(page, "insecure-transport");
    await dlg.getByLabel("Your password").fill(PASSWORD);
    await upload(page).click();
    const ask = page.getByRole("dialog", { name: "This connection is not encrypted" });
    await expect(ask).toBeVisible();
    await ask.getByRole("button", { name: "Cancel" }).click();
    await expect(dlg.locator("[data-validator-error]")).toContainText(/nothing was sent/i);
    await upload(page).click();
    await page.getByRole("dialog", { name: "This connection is not encrypted" }).getByRole("button", { name: "Continue anyway" }).click();
    await expect(dialog(page)).toHaveCount(0);
  });

  test("the password is forgotten once it has been sent", async ({ page }) => {
    const dlg = await filled(page, "local-account-profile");
    await dlg.getByLabel("Your password").fill("not-the-password");
    await upload(page).click();
    await expect(dlg.locator("[data-validator-error]")).toBeVisible();
    await expect(dlg.getByLabel("Your password")).toHaveValue("");
    await expect(upload(page)).toBeDisabled();
  });

  test("a shut gate is said before anything is pressed, with the command for the host", async ({ page }) => {
    const dlg = await filled(page, "gateway-profile");
    const notice = dlg.locator("[data-gate-notice]");
    await expect(notice).toBeVisible();
    await expect(notice).toContainText("retnd assurance validators add postgres-dump");
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      expect(await measureContrast(page, "1203 shut gate " + theme, "[data-gate-notice]"), theme).toEqual([]);
    }
  });

  test("pressing upload on a shut gate is refused and nothing is added", async ({ page }) => {
    const dlg = await filled(page, "gate-shut");
    await upload(page).click();
    const refusal = dlg.locator("[data-gate-refusal]");
    await expect(refusal).toContainText("The validator was not uploaded");
    await expect(refusal).toContainText("retnd assurance validators add postgres-dump");
    await expect(card(page).getByRole("row").filter({ hasText: "postgres-dump" })).toHaveCount(0);
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      expect(await measureContrast(page, "1203 gate refusal " + theme, "[data-gate-refusal]"), theme).toEqual([]);
    }
  });

  test("editing takes the password too", async ({ page }) => {
    await open(page, "/settings", "local-account-profile");
    await card(page).getByRole("button", { name: "Edit any-tar" }).click();
    const dlg = page.getByRole("dialog", { name: "Edit validator" });
    await dlg.getByLabel("Artifact-name pattern").fill(String.raw`^auth-config-\d{8}\.tar\.zst$`);
    const save = dlg.getByRole("button", { name: "Save changes" });
    await expect(save).toBeDisabled();
    await dlg.getByLabel("Your password").fill(PASSWORD);
    await save.click();
    await expect(dlg).toHaveCount(0);
    await expect(card(page).getByRole("row").filter({ hasText: "any-tar" })).toContainText(String.raw`^auth-config-\d{8}\.tar\.zst$`);
  });
});
