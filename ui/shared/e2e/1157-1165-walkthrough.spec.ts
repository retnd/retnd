/**
 * Slow passes over the #1157 and #1165 surfaces for the GIFs, one per theme.
 * Same scenario as 1165-gate-notice.spec.ts: the engine reports its
 * destructive gate shut with a reason, and every screen that presses a gated
 * act is walked once. Each stop says the gate is shut before anything is
 * pressed, then presses the control and ends on the refusal, so the recording
 * is of the corrected behaviour and cannot be of a tree that lacks it.
 * Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, setTheme, spaGo } from "./helpers";

const SET = "production/postgres-primary";
const RUN = "run-2026-09-13-0400";
const beat = async (p: Page, ms = 1300) => {
  await p.mouse.move(1270, 5);
  await p.waitForTimeout(ms);
};

test.use({ viewport: { width: 1280, height: 900 }, video: process.env.E2E_VIDEO ? { mode: "on", size: { width: 1280, height: 900 } } : "off" });

for (const theme of ["light", "dark"] as const) {
  test(`1157 and 1165 walkthrough (${theme}): list, one snapshot, restore, retention preview`, async ({ page }) => {
    test.setTimeout(180_000);
    const notice = page.locator("[data-gate-notice]");
    const refusal = page.locator("[data-gate-refusal]");

    // The snapshot list: said before the press, then refused after it.
    await open(page, `/sets/${SET}/snapshots`, "gate-shut-local-account");
    await setTheme(page, theme);
    await expect(notice).toBeVisible();
    await beat(page, 2600);
    await page.getByRole("button", { name: "Verify latest" }).click();
    await expect(refusal).toContainText("retnd snapshot verify " + SET);
    await beat(page, 3000);

    // One snapshot: verify, then a hold with the reason that was typed.
    await spaGo(page, `/sets/${SET}/snapshots/${RUN}`);
    await setTheme(page, theme);
    await expect(notice).toBeVisible();
    await beat(page, 1800);
    await page.getByRole("button", { name: "Verify now" }).click();
    await expect(refusal).toContainText("--run " + RUN);
    await beat(page, 2400);
    await page.getByRole("button", { name: /Place hold/ }).click();
    await page.getByLabel("Reason").fill("legal-review");
    await beat(page, 900);
    await page.getByRole("button", { name: "Place hold", exact: true }).click();
    await expect(refusal.last()).toContainText("--reason legal-review");
    await beat(page, 3000);

    // The restore wizard: four steps, then the refusal with their answers.
    await spaGo(page, `/sets/${SET}/restore`);
    await setTheme(page, theme);
    await expect(notice).toBeVisible();
    await beat(page, 1800);
    await page.getByRole("button", { name: "Continue" }).click();
    await beat(page, 600);
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByLabel("Restore into").fill("/srv/restored");
    await beat(page, 600);
    await page.getByRole("button", { name: "Continue" }).click();
    await beat(page, 800);
    await page.getByRole("button", { name: "Start restore" }).click();
    await expect(refusal).toContainText("--to /srv/restored");
    await beat(page, 3200);

    // The retention preview: the dialog says so, then applying is refused.
    await spaGo(page, `/sets/${SET}`);
    await setTheme(page, theme);
    await page.getByRole("button", { name: "Preview retention plan" }).click();
    const dialog = page.getByRole("dialog", { name: "Retention preview" });
    await expect(dialog.locator("[data-gate-notice]")).toBeVisible();
    await beat(page, 2600);
    await dialog.getByRole("button", { name: /^Continue/ }).click();
    await page.getByRole("button", { name: /^Delete \d+ backups$/ }).click();
    await expect(dialog.locator("[data-gate-refusal]")).toContainText("retnd retention apply " + SET + " --acknowledge");
    await beat(page, 3400);
  });
}
