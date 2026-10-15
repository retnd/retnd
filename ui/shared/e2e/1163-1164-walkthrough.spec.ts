/**
 * Slow passes over the #1163 and #1164 surfaces for the GIFs, one per theme.
 *
 * Three recordings per theme. The first is a local-account profile, where the
 * destructive gate is open: the snapshot list verifies, the retention plan
 * applies, and the restore wizard asks for the password and a tick before it
 * will start. The second is a gateway profile, where the gate is still shut:
 * verify, hold and release-hold work anyway (and releasing waits for its
 * tick), and the restore screen says the gate is shut before anything is
 * pressed and ends on the refusal with the command. The third (the review of
 * PR #1167) is a local-account profile reached over plain HTTP from another
 * machine: the restore asks about the connection before the password is sent,
 * a no sends nothing, and a yes sends it with the consent.
 *
 * Every stop ends on the outcome, so the recording is of the behaviour and
 * cannot be of a tree that lacks it. Run with E2E_VIDEO=1.
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
  test(`1163 walkthrough (${theme}): a local-account profile verifies, applies retention and restores`, async ({ page }) => {
    test.setTimeout(180_000);
    const refusal = page.locator("[data-gate-refusal]");

    // Verify from the snapshot list.
    await open(page, `/sets/${SET}/snapshots`, "local-account-profile");
    await setTheme(page, theme);
    await expect(page.getByRole("button", { name: "Verify latest" })).toBeVisible();
    await beat(page, 1600);
    await page.getByRole("button", { name: "Verify latest" }).click();
    await expect(page.getByRole("region", { name: "Verification in progress" })).toBeVisible();
    await beat(page, 2600);

    // Apply a retention plan.
    await spaGo(page, `/sets/${SET}`);
    await setTheme(page, theme);
    await page.getByRole("button", { name: "Preview retention plan" }).click();
    const dialog = page.getByRole("dialog", { name: "Retention preview" });
    await expect(dialog).toBeVisible();
    await expect(dialog.locator("[data-gate-notice]")).toHaveCount(0);
    await beat(page, 1800);
    await dialog.getByRole("button", { name: /^Continue/ }).click();
    await beat(page, 1200);
    await page.getByRole("button", { name: /^Delete \d+ backups$/ }).click();
    await expect(dialog).toHaveCount(0);
    await expect(refusal).toHaveCount(0);
    await beat(page, 2200);

    // Restore: the password and the tick, a wrong password, then a right one.
    await spaGo(page, `/sets/${SET}/restore`);
    await setTheme(page, theme);
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByLabel("Restore into").fill("/srv/restored");
    await beat(page, 600);
    await page.getByRole("button", { name: "Continue" }).click();
    await expect(page.getByLabel("Your password")).toBeVisible();
    await beat(page, 1800);
    await page.getByLabel("Your password").fill("not the password");
    await page.getByRole("checkbox", { name: /I understand/ }).check();
    await beat(page, 800);
    await page.getByRole("button", { name: "Start restore" }).click();
    await expect(page.locator("[data-step-up-error]")).toBeVisible();
    await beat(page, 2600);
    await page.getByLabel("Your password").fill("correct-horse-battery");
    await beat(page, 600);
    await page.getByRole("button", { name: "Start restore" }).click();
    await expect(page.locator("[data-step-up-error]")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Start restore" })).toBeDisabled();
    await beat(page, 3000);
  });

  test(`1163 walkthrough (${theme}): over plain HTTP from another machine, a restore asks before the password leaves`, async ({ page }) => {
    test.setTimeout(180_000);
    const DIALOG = "This connection is not encrypted";
    await open(page, `/sets/${SET}/restore`, "insecure-transport");
    await setTheme(page, theme);
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByLabel("Restore into").fill("/srv/restored");
    await beat(page, 600);
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByLabel("Your password").fill("correct-horse-battery");
    await page.getByRole("checkbox", { name: /I understand/ }).check();
    await beat(page, 1200);
    await page.getByRole("button", { name: "Start restore" }).click();
    // Asked first. A no sends nothing and keeps nothing.
    await expect(page.getByRole("dialog", { name: DIALOG })).toBeVisible();
    await beat(page, 2600);
    await page.getByRole("dialog", { name: DIALOG }).getByRole("button", { name: "Cancel" }).click();
    await expect(page.locator("[data-step-up-error]")).toContainText(/nothing was sent/i);
    await expect(page.getByLabel("Your password")).toHaveValue("");
    await beat(page, 2600);
    // A yes, and only then the password goes, with the consent beside it.
    await page.getByLabel("Your password").fill("correct-horse-battery");
    await beat(page, 600);
    await page.getByRole("button", { name: "Start restore" }).click();
    await expect(page.getByRole("dialog", { name: DIALOG })).toBeVisible();
    await beat(page, 1600);
    await page.getByRole("dialog", { name: DIALOG }).getByRole("button", { name: "Continue anyway" }).click();
    await expect(page.locator("[data-step-up-error]")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Start restore" })).toBeDisabled();
    await beat(page, 3000);
  });

  test(`1164 walkthrough (${theme}): a gateway profile verifies, holds and releases, and restore stays gated`, async ({ page }) => {
    test.setTimeout(180_000);
    const notice = page.locator("[data-gate-notice]");
    const refusal = page.locator("[data-gate-refusal]");

    // The list: no notice, and verify works on a shut gate.
    await open(page, `/sets/${SET}/snapshots`, "gateway-profile");
    await setTheme(page, theme);
    await expect(page.getByRole("button", { name: "Verify latest" })).toBeVisible();
    await expect(notice).toHaveCount(0);
    await beat(page, 1600);
    await page.getByRole("button", { name: "Verify latest" }).click();
    await expect(page.getByRole("region", { name: "Verification in progress" })).toBeVisible();
    await beat(page, 2400);

    // One snapshot: a hold, then a release that waits for its tick.
    await spaGo(page, `/sets/${SET}/snapshots/${RUN}`);
    await setTheme(page, theme);
    await expect(notice).toHaveCount(0);
    await beat(page, 1200);
    await page.getByRole("button", { name: /Place hold/ }).click();
    await page.getByLabel("Reason").fill("legal-review");
    await beat(page, 800);
    await page.getByRole("button", { name: "Place hold", exact: true }).click();
    await expect(page.getByRole("button", { name: /^Release hold / })).toBeVisible();
    await beat(page, 1800);
    await page.getByRole("button", { name: /^Release hold / }).click();
    const release = page.getByRole("dialog", { name: "Release this hold" });
    await expect(release.getByRole("button", { name: "Release hold" })).toBeDisabled();
    await beat(page, 2400);
    await release.getByRole("checkbox", { name: /I understand/ }).check();
    await beat(page, 900);
    await release.getByRole("button", { name: "Release hold" }).click();
    await expect(page.getByRole("button", { name: /^Release hold / })).toHaveCount(0);
    await beat(page, 2200);

    // Restore is still behind the gate: said first, refused after.
    await spaGo(page, `/sets/${SET}/restore`);
    await setTheme(page, theme);
    await expect(notice).toBeVisible();
    await beat(page, 2200);
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByLabel("Restore into").fill("/srv/restored");
    await page.getByRole("button", { name: "Continue" }).click();
    await beat(page, 800);
    await page.getByRole("button", { name: "Start restore" }).click();
    await expect(refusal).toContainText("--to /srv/restored");
    await beat(page, 3000);
  });
}
