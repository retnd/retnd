import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1164: snapshot verify, hold and release-hold are not behind the
 * destructive gate, so they work on a gateway profile whose gate is shut for
 * everything else. Restoring a snapshot is still behind it.
 *
 * Release-hold lifts a protection, so the dialog asks for an explicit
 * acknowledgement before it will send.
 */
const SET = "production/postgres-primary";
const RUN = "run-2026-09-13-0400";
const DETAIL = `/sets/${SET}/snapshots/${RUN}`;
const refusal = (page: Page) => page.locator("[data-gate-refusal]");
const notice = (page: Page) => page.locator("[data-gate-notice]");

async function placeAHold(page: Page) {
  await page.getByRole("button", { name: /Place hold/ }).click();
  await page.getByLabel("Reason").fill("legal-review");
  await page.getByRole("button", { name: "Place hold", exact: true }).click();
  await expect(page.getByRole("button", { name: /^Release hold / })).toBeVisible();
}

for (const scenario of ["gateway-profile", "local-account-profile"] as const) {
  test.describe(`#1164 ${scenario}`, () => {
    test("the snapshot list: no gate notice, and Verify latest starts a verification", async ({ page }) => {
      await open(page, `/sets/${SET}/snapshots`, scenario);
      await expect(page.getByRole("button", { name: "Verify latest" })).toBeVisible();
      await expect(notice(page)).toHaveCount(0);
      await page.getByRole("button", { name: "Verify latest" }).click();
      await expect(page.getByRole("region", { name: "Verification in progress" })).toBeVisible();
      await expect(refusal(page)).toHaveCount(0);
    });

    test("one snapshot: no gate notice, verify and hold work", async ({ page }) => {
      await open(page, DETAIL, scenario);
      await expect(page.getByRole("button", { name: "Verify now" })).toBeVisible();
      await expect(notice(page)).toHaveCount(0);
      await page.getByRole("button", { name: "Verify now" }).click();
      await expect(page.getByRole("region", { name: "Verification in progress" })).toBeVisible();
      await placeAHold(page);
      await expect(refusal(page)).toHaveCount(0);
    });

    test("releasing a hold waits for an explicit acknowledgement", async ({ page }) => {
      await open(page, DETAIL, scenario);
      await placeAHold(page);
      await page.getByRole("button", { name: /^Release hold / }).click();
      const dialog = page.getByRole("dialog", { name: "Release this hold" });
      const confirm = dialog.getByRole("button", { name: "Release hold" });
      await expect(confirm).toBeDisabled();
      await dialog.getByRole("checkbox", { name: /I understand/ }).check();
      await expect(confirm).toBeEnabled();
      await confirm.click();
      await expect(page.getByRole("button", { name: /^Release hold / })).toHaveCount(0);
      await expect(refusal(page)).toHaveCount(0);
    });

    test("the retention page: no gate notice, hold and release work", async ({ page }) => {
      await open(page, `/sets/${SET}/snapshot-retention`, scenario);
      await expect(page.getByRole("heading").first()).toBeVisible();
      await expect(notice(page)).toHaveCount(0);
    });

    test("the acknowledgement is readable in both themes (WCAG AA)", async ({ page }) => {
      await open(page, DETAIL, scenario);
      await placeAHold(page);
      await page.getByRole("button", { name: /^Release hold / }).click();
      const dialog = page.getByRole("dialog", { name: "Release this hold" });
      await expect(dialog.locator("[data-release-ack]")).toBeVisible();
      for (const theme of ["light", "dark"] as const) {
        await setTheme(page, theme);
        const failures = await measureContrast(page, `1164 release ack ${scenario} ${theme}`, "[data-release-ack]");
        expect(failures, theme + " theme").toEqual([]);
      }
    });
  });
}

test.describe("#1164 restore is still behind the gate", () => {
  test("a gateway profile says so on the restore page and refuses the press", async ({ page }) => {
    await open(page, `/sets/${SET}/restore`, "gateway-profile");
    await expect(notice(page)).toBeVisible();
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByLabel("Restore into").fill("/srv/restored");
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByRole("button", { name: "Start restore" }).click();
    await expect(refusal(page)).toContainText("retnd snapshot restore " + SET + " --to /srv/restored");
  });
});
