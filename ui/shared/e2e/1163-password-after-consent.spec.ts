import { expect, test } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1163, from the review of PR #1167: on a connection that is not
 * encrypted, the administrator's password must never leave the browser before
 * the operator has been told the connection is not encrypted and said go on.
 *
 * `wire-restore=1` sends the restore through the production client
 * (api/client.ts) instead of the mock, so this spec reads the real request
 * the browser makes, headers and all. Everything else on the page is still the
 * mock's `insecure-transport` scenario: a local-account profile whose
 * capabilities say this connection needs the extra acknowledgement.
 */
const SET = "production/postgres-primary";
const PASSWORD = "correct-horse-battery";
const DIALOG = "This connection is not encrypted";

async function walkToReview(page: import("@playwright/test").Page) {
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByLabel("Restore into").fill("/srv/restored");
  await page.getByRole("button", { name: "Continue" }).click();
}

test.describe("#1163 the password never leaves before the transport consent", () => {
  test("the restore asks first, a no sends nothing, and a yes sends the password with the acknowledgement", async ({ page }) => {
    const sent: Array<Record<string, string>> = [];
    await page.route("**/api/v1/operations", async (route) => {
      if (route.request().method() !== "POST") return route.continue();
      sent.push(route.request().headers());
      // A refusal keeps the page where it is, which is all this spec needs.
      await route.fulfill({
        status: 403,
        json: { error: { code: "STEP_UP_FAILED", message: "that password was not accepted", correlation_id: "cid_e2e" } }
      });
    });

    await open(page, `/sets/${SET}/restore?wire-restore=1`, "insecure-transport");
    await walkToReview(page);
    await page.getByRole("checkbox", { name: /I understand/ }).check();
    await page.getByLabel("Your password").fill(PASSWORD);
    await page.getByRole("button", { name: "Start restore" }).click();

    const dialog = page.getByRole("dialog", { name: DIALOG });
    await expect(dialog, "the question comes before the request").toBeVisible();
    expect(sent, "nothing goes out while the question is open").toHaveLength(0);

    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(page.locator("[data-step-up-error]")).toContainText(/nothing was sent/i);
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      const failures = await measureContrast(page, "1163 nothing sent " + theme, "[data-step-up-error]");
      expect(failures, theme + " theme").toEqual([]);
    }
    await page.waitForTimeout(400);
    expect(sent, "a no sends nothing at all").toHaveLength(0);
    await expect(page.getByLabel("Your password"), "the password is not kept after a submit").toHaveValue("");

    await page.getByLabel("Your password").fill(PASSWORD);
    await page.getByRole("button", { name: "Start restore" }).click();
    await page.getByRole("dialog", { name: DIALOG }).getByRole("button", { name: "Continue anyway" }).click();
    await expect.poll(() => sent.length).toBe(1);
    expect(sent[0]["x-retnd-insecure-transport-ack"], "the first request that carries the password carries the consent").toBe(
      "acknowledged"
    );
    expect(sent[0]["x-retnd-step-up-password"]).toBe(PASSWORD);
    await expect(page.getByLabel("Your password"), "cleared after a refused submit too").toHaveValue("");
  });

  test("control: on an encrypted or same-machine connection the restore is not asked about transport", async ({ page }) => {
    const sent: Array<Record<string, string>> = [];
    await page.route("**/api/v1/operations", async (route) => {
      if (route.request().method() !== "POST") return route.continue();
      sent.push(route.request().headers());
      await route.fulfill({
        status: 403,
        json: { error: { code: "STEP_UP_FAILED", message: "that password was not accepted", correlation_id: "cid_e2e" } }
      });
    });
    await open(page, `/sets/${SET}/restore?wire-restore=1`, "local-account-profile");
    await walkToReview(page);
    await page.getByRole("checkbox", { name: /I understand/ }).check();
    await page.getByLabel("Your password").fill(PASSWORD);
    await page.getByRole("button", { name: "Start restore" }).click();
    await expect.poll(() => sent.length).toBe(1);
    await expect(page.getByRole("dialog", { name: DIALOG })).toHaveCount(0);
    expect(sent[0]["x-retnd-insecure-transport-ack"]).toBeUndefined();
  });
});

test.describe("#1163 a tripped password limiter says wait, not check the password", () => {
  test("the step-up error tells the operator to wait", async ({ page }) => {
    await open(page, `/sets/${SET}/restore`, "step-up-limited");
    await walkToReview(page);
    await page.getByRole("checkbox", { name: /I understand/ }).check();
    await page.getByLabel("Your password").fill(PASSWORD);
    await page.getByRole("button", { name: "Start restore" }).click();
    const alert = page.locator("[data-step-up-error]");
    await expect(alert).toBeVisible();
    await expect(alert).toContainText(/wait/i);
    await expect(alert).not.toContainText(/check it/i);
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      const failures = await measureContrast(page, "1163 rate limited " + theme, "[data-step-up-error]");
      expect(failures, theme + " theme").toEqual([]);
    }
  });
});
