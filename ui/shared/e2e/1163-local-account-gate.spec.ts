import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1163: the destructive gate is open on a local-account profile, so the
 * screens that press a gated act work there, and they still say the gate is
 * shut on a gateway profile.
 *
 * `scenario=local-account-profile` is what a local-account deployment reports:
 * destructive_operations open, and restores asking for the administrator's
 * password again. `scenario=gateway-profile` is what a UGOS deployment
 * reports: shut, with the reason the engine gives for a gateway profile. The
 * mock enforces the engine's own rules in both (step-up and acknowledgement
 * for a restore, the refusal for a shut gate), so a spec fails when a screen
 * stops sending what the engine asks for, not only when a label changes.
 */
const SET = "production/postgres-primary";
const PASSWORD = "correct-horse-battery";
const refusal = (page: Page) => page.locator("[data-gate-refusal]");
const notice = (page: Page) => page.locator("[data-gate-notice]");

async function applyTheFirstPlan(page: Page) {
  await page.getByRole("button", { name: "Preview retention plan" }).click();
  const dialog = page.getByRole("dialog", { name: "Retention preview" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: /^Continue/ }).click();
  await page.getByRole("button", { name: /^Delete \d+ backups$/ }).click();
}

async function walkToReview(page: Page) {
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByLabel("Restore into").fill("/srv/restored");
  await page.getByRole("button", { name: "Continue" }).click();
}

test.describe("#1163 a local-account profile", () => {
  test("retention apply runs from the UI and says nothing about a shut gate", async ({ page }) => {
    await open(page, `/sets/${SET}`, "local-account-profile");
    await applyTheFirstPlan(page);
    await expect(page.getByRole("dialog", { name: "Retention preview" })).toHaveCount(0);
    await expect(refusal(page)).toHaveCount(0);
    await expect(notice(page)).toHaveCount(0);
  });

  test("snapshot verify runs from the UI", async ({ page }) => {
    await open(page, `/sets/${SET}/snapshots`, "local-account-profile");
    await page.getByRole("button", { name: "Verify latest" }).click();
    await expect(page.getByRole("region", { name: "Verification in progress" })).toBeVisible();
    await expect(refusal(page)).toHaveCount(0);
  });

  test("a restore cannot start without the password and the acknowledgement", async ({ page }) => {
    await open(page, `/sets/${SET}/restore`, "local-account-profile");
    await walkToReview(page);
    const start = page.getByRole("button", { name: "Start restore" });
    await expect(page.getByLabel("Your password")).toBeVisible();
    await expect(start).toBeDisabled();
    await page.getByLabel("Your password").fill(PASSWORD);
    await expect(start, "the password alone is not the acknowledgement").toBeDisabled();
    await page.getByRole("checkbox", { name: /I understand/ }).check();
    await expect(start).toBeEnabled();
  });

  test("a restore with both starts, and a wrong password is said plainly and starts nothing", async ({ page }) => {
    await open(page, `/sets/${SET}/restore`, "local-account-profile");
    await walkToReview(page);
    await page.getByRole("checkbox", { name: /I understand/ }).check();
    await page.getByLabel("Your password").fill("not the password");
    await page.getByRole("button", { name: "Start restore" }).click();
    const alert = page.locator("[data-step-up-error]");
    await expect(alert).toBeVisible();
    await expect(alert).toContainText(/password/i);
    await expect(refusal(page)).toHaveCount(0);

    await page.getByLabel("Your password").fill(PASSWORD);
    await page.getByRole("button", { name: "Start restore" }).click();
    await expect(page.locator("[data-step-up-error]")).toHaveCount(0);
    await expect(page.getByText(/restore/i).first()).toBeVisible();
  });

  test("the step-up words are readable in both themes (WCAG AA)", async ({ page }) => {
    await open(page, `/sets/${SET}/restore`, "local-account-profile");
    await walkToReview(page);
    await page.getByRole("checkbox", { name: /I understand/ }).check();
    await page.getByLabel("Your password").fill("not the password");
    await page.getByRole("button", { name: "Start restore" }).click();
    await expect(page.locator("[data-step-up-error]")).toBeVisible();
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      const failures = await measureContrast(page, "1163 step-up " + theme, "[data-step-up]");
      expect(failures, theme + " theme").toEqual([]);
      const errors = await measureContrast(page, "1163 step-up error " + theme, "[data-step-up-error]");
      expect(errors, theme + " theme").toEqual([]);
    }
  });
});

test.describe("#1163 a gateway profile", () => {
  test("the gate is reported shut with the gateway reason, and the notice names the command", async ({ page }) => {
    await open(page, `/sets/${SET}/restore`, "gateway-profile");
    await expect(notice(page)).toBeVisible();
    await expect(notice(page)).toContainText("gateway profile");
    await expect(notice(page)).toContainText("retnd snapshot restore " + SET + " --to");
    await expect(page.getByLabel("Your password"), "a shut gate asks for no password").toHaveCount(0);
  });

  test("retention apply says the gate is shut before and after the press", async ({ page }) => {
    await open(page, `/sets/${SET}`, "gateway-profile");
    await page.getByRole("button", { name: "Preview retention plan" }).click();
    const dialog = page.getByRole("dialog", { name: "Retention preview" });
    await expect(dialog.locator("[data-gate-notice]")).toContainText("gateway profile");
    await dialog.getByRole("button", { name: /^Continue/ }).click();
    await page.getByRole("button", { name: /^Delete \d+ backups$/ }).click();
    await expect(refusal(page)).toContainText(/nothing was deleted/i);
    await expect(refusal(page)).toContainText("retnd retention apply " + SET + " --acknowledge");
  });

  test("the words are readable in both themes (WCAG AA)", async ({ page }) => {
    await open(page, `/sets/${SET}/restore`, "gateway-profile");
    await expect(notice(page)).toBeVisible();
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      const failures = await measureContrast(page, "1163 gateway notice " + theme, "[data-gate-notice]");
      expect(failures, theme + " theme").toEqual([]);
    }
  });
});
