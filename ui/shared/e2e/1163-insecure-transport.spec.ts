import { expect, test } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1163: a local-account profile reached over plain HTTP from another
 * machine. The engine's session cookie cannot be Secure there, so a gated act
 * wants one more acknowledgement, and the operator is asked in words before it
 * goes. `scenario=insecure-transport` has the mock do what api/client.ts does
 * against a real engine: the first gated act raises the dialog, a no leaves
 * the engine's refusal on screen, and a yes lets the act through.
 */
const DIALOG = "This connection is not encrypted";

test.describe("#1163 an unencrypted connection", () => {
  test("a no leaves the refusal, a yes lets the run through", async ({ page }) => {
    await open(page, "/", "insecure-transport");
    await page.getByRole("button", { name: "Run all enabled sets" }).click();
    const dialog = page.getByRole("dialog", { name: DIALOG });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText(/password/i);

    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByText(/is not encrypted and is not from this machine/).first()).toBeVisible();

    await page.getByRole("button", { name: "Run all enabled sets" }).click();
    await expect(page.getByRole("dialog", { name: DIALOG })).toBeVisible();
    await page.getByRole("dialog", { name: DIALOG }).getByRole("button", { name: "Continue anyway" }).click();
    await expect(page.getByRole("dialog", { name: DIALOG })).toHaveCount(0);

    // A yes holds for the page load, as api/client.ts holds it: pressing again
    // goes straight through, with no second question.
    await page.getByRole("button", { name: "Run all enabled sets" }).click();
    await page.waitForTimeout(600);
    await expect(page.getByRole("dialog", { name: DIALOG })).toHaveCount(0);
  });

  test("control: an encrypted or same-machine connection is never asked", async ({ page }) => {
    await open(page, "/", "local-account-profile");
    await page.getByRole("button", { name: "Run all enabled sets" }).click();
    await expect(page.getByRole("dialog", { name: DIALOG })).toHaveCount(0);
  });

  test("the dialog is readable in both themes (WCAG AA)", async ({ page }) => {
    await open(page, "/", "insecure-transport");
    await page.getByRole("button", { name: "Run all enabled sets" }).click();
    await expect(page.getByRole("dialog", { name: DIALOG })).toBeVisible();
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      const failures = await measureContrast(page, "1163 insecure transport " + theme, "[role=dialog]");
      expect(failures, theme + " theme").toEqual([]);
    }
  });
});
