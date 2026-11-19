/**
 * Issue #1203, the upload dialog on Settings. Nobody should have to write a
 * regex by hand: the app offers the patterns it derives from the backups it
 * already holds, says what a pattern would match before it is saved (judged by
 * the engine's RE2, not the browser's), and refuses early what the engine will
 * refuse. The script can be typed, uploaded, or started from the documented
 * default.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureBorders, measureContrast, open, setTheme } from "./helpers";

const BILLING = String.raw`^billing-\d{8}\.sql\.gz$`;
const POSTGRES = String.raw`^postgres-prod-\d{8}\.dump\.zst$`;
const card = (p: Page) => p.getByRole("region", { name: "Recovery-test validators" });
const dialog = (p: Page) => p.getByRole("dialog", { name: "Add validator" });

async function openAdd(page: Page, scenario = "default") {
  await open(page, "/settings", scenario);
  await card(page).getByRole("button", { name: "Add validator" }).click();
  await expect(dialog(page)).toBeVisible();
  return dialog(page);
}

test.describe("#1203 Add validator", () => {
  test("offers the patterns it can derive, most common first, with how many backups each matches", async ({ page }) => {
    const dlg = await openAdd(page);
    const radios = dlg.getByRole("group", { name: "Suggested patterns" }).getByRole("radio");
    await expect(radios.first()).toHaveAccessibleName(new RegExp(BILLING.replace(/[\\^$.{}]/g, "\\$&")));
    await expect(radios.first()).toHaveAccessibleName(/matches 2 backups/);
    await expect(radios.first()).toHaveAccessibleName(/billing-20260827\.sql\.gz/);
    await expect(dlg.getByRole("radio", { name: POSTGRES })).toHaveAccessibleName(/matches 1 backup\b/);
  });

  test("says which suggestion already has a validator, at the scope it is on", async ({ page }) => {
    const dlg = await openAdd(page);
    // billing-dump is scoped to one set, so it is on that set's pattern and
    // not on the same pattern offered for every set.
    await expect(dlg.getByRole("radio", { name: BILLING })).not.toHaveAccessibleName(/already checked/);
    await dlg.getByLabel("Applies to").selectOption("production/billing-mysql");
    await expect(dlg.getByRole("radio", { name: BILLING })).toHaveAccessibleName(/already checked by billing-dump/);
  });

  test("choosing a suggestion fills the pattern and previews what it matches", async ({ page }) => {
    const dlg = await openAdd(page);
    await dlg.getByRole("radio", { name: POSTGRES }).check();
    await expect(dlg.getByLabel("Artifact-name pattern")).toHaveValue(POSTGRES);
    const preview = dlg.locator("[data-validator-preview]");
    await expect(preview).toContainText("Matches 1 backup");
    await expect(preview).toContainText("postgres-prod-20260828.dump.zst");
  });

  test("narrowing to a backup set narrows the suggestions to that set", async ({ page }) => {
    const dlg = await openAdd(page);
    await dlg.getByLabel("Applies to").selectOption("production/postgres-primary");
    await expect(dlg.getByRole("radio", { name: POSTGRES })).toBeVisible();
    await expect(dlg.getByRole("radio", { name: BILLING })).toHaveCount(0);
  });

  test("a pattern the engine cannot parse is refused with the engine's reason", async ({ page }) => {
    const dlg = await openAdd(page);
    await dlg.getByLabel("Artifact-name pattern").fill("^(?=x)$");
    const preview = dlg.locator("[data-validator-preview]");
    await expect(preview).toContainText("invalid or unsupported Perl syntax");
    await expect(preview.getByRole("alert")).toBeVisible();
    await expect(dlg.getByRole("button", { name: "Upload validator" })).toBeDisabled();
  });

  test("a pattern that is not anchored says it matches anywhere in a name", async ({ page }) => {
    const dlg = await openAdd(page);
    await dlg.getByLabel("Artifact-name pattern").fill(String.raw`\.dump\.zst`);
    await expect(dlg.locator("[data-validator-preview]")).toContainText("not anchored");
  });

  test("a pattern an existing validator at the same scope already matches is refused before upload", async ({ page }) => {
    const dlg = await openAdd(page);
    await dlg.getByLabel("Applies to").selectOption("production/billing-mysql");
    await dlg.getByLabel("Artifact-name pattern").fill(String.raw`^billing-\d+\.sql\.gz$`);
    const preview = dlg.locator("[data-validator-preview]");
    await expect(preview).toContainText("billing-dump");
    await expect(preview).toContainText("billing-20260827.sql.gz");
    await expect(preview).toContainText("two validators");
    await expect(dlg.getByRole("button", { name: "Upload validator" })).toBeDisabled();
    // The same pattern with no set is a different scope, so it is not a collision.
    await dlg.getByLabel("Applies to").selectOption("");
    await expect(preview).not.toContainText("two validators");
  });

  test("refuses names the engine refuses", async ({ page }) => {
    const dlg = await openAdd(page);
    const name = dlg.getByLabel("Name", { exact: true });
    await name.fill("retnd-mine");
    await expect(dlg.locator("[data-validator-field-problem]")).toContainText("ids beginning retnd- are the product's own built-in checks");
    await name.fill("has space");
    await expect(dlg.locator("[data-validator-field-problem]")).toContainText("bare identifier");
    await name.fill("mine.sh");
    await expect(dlg.locator("[data-validator-field-problem]")).toContainText("without its .local.sh suffix");
    await name.fill("mine");
    await expect(dlg.locator("[data-validator-field-problem]")).toHaveCount(0);
  });

  test("offers the documented default script to read, and to start from", async ({ page }) => {
    const dlg = await openAdd(page);
    const download = page.waitForEvent("download");
    await dlg.getByRole("button", { name: "Download default script" }).click();
    expect((await download).suggestedFilename()).toMatch(/\.local\.sh$/);

    await expect(dlg.getByLabel("Script", { exact: true })).toHaveValue("");
    await dlg.getByRole("button", { name: "Start from the default script" }).click();
    await expect(dlg.getByLabel("Script", { exact: true })).toHaveValue(/^#!\/usr\/bin\/env bash/);
  });

  test("takes a script from a file and says how big it is", async ({ page }) => {
    const dlg = await openAdd(page);
    await dlg.getByLabel("Script file").setInputFiles({ name: "mine.local.sh", mimeType: "text/x-shellscript", buffer: Buffer.from("#!/bin/sh\nexit 0\n") });
    await expect(dlg.getByLabel("Script", { exact: true })).toHaveValue("#!/bin/sh\nexit 0\n");
    await expect(dlg.locator("[data-script-size]")).toContainText("17 bytes");
  });

  test("refuses a script larger than the deployment allows", async ({ page }) => {
    const dlg = await openAdd(page);
    await dlg.getByLabel("Script file").setInputFiles({ name: "big.sh", mimeType: "text/plain", buffer: Buffer.from("#!/bin/sh\n" + "# pad\n".repeat(50_000)) });
    await expect(dlg.locator("[data-validator-file-problem]")).toContainText("this deployment takes at most 256 KiB");
    await expect(dlg.getByLabel("Script", { exact: true })).toHaveValue("");
    await expect(dlg.getByRole("button", { name: "Upload validator" })).toBeDisabled();
  });

  test("uploads a validator and lists it", async ({ page }) => {
    const dlg = await openAdd(page);
    await dlg.getByLabel("Name", { exact: true }).fill("postgres-dump");
    await dlg.getByRole("radio", { name: POSTGRES }).check();
    await dlg.getByRole("button", { name: "Start from the default script" }).click();
    // No password is asked of a deployment that does not re-check it.
    await expect(dlg.getByLabel("Your password")).toHaveCount(0);
    await dlg.getByRole("button", { name: "Upload validator" }).click();
    await expect(dialog(page)).toHaveCount(0);
    const row = card(page).getByRole("row").filter({ hasText: "postgres-dump" });
    await expect(row).toContainText(POSTGRES);
    await expect(row).toContainText("Every backup set");
    await expect(row).toContainText("1 backup");
    await expect(card(page).locator("[data-validators-status]")).toContainText("Uploaded postgres-dump");
  });

  test("a name that is taken is the engine's refusal, shown in the dialog", async ({ page }) => {
    const dlg = await openAdd(page);
    await dlg.getByLabel("Name", { exact: true }).fill("billing-dump");
    await dlg.getByRole("radio", { name: POSTGRES }).check();
    await dlg.getByRole("button", { name: "Start from the default script" }).click();
    await dlg.getByRole("button", { name: "Upload validator" }).click();
    await expect(dlg.locator("[data-validator-error]")).toContainText("already a validator named billing-dump");
    await expect(dialog(page)).toBeVisible();
  });

  test("a deployment at its limit says so", async ({ page }) => {
    const dlg = await openAdd(page, "validators-full");
    await expect(dlg.locator("[data-validator-limit]")).toContainText("64");
    await expect(dlg.getByRole("button", { name: "Upload validator" })).toBeDisabled();
  });

  test("Escape closes it and returns focus to the button that opened it", async ({ page }) => {
    await openAdd(page);
    await page.keyboard.press("Escape");
    await expect(dialog(page)).toHaveCount(0);
    await expect(card(page).getByRole("button", { name: "Add validator" })).toBeFocused();
  });

  test("editing keeps the name, takes a new pattern, and leaves the script alone", async ({ page }) => {
    await open(page, "/settings");
    await card(page).getByRole("button", { name: "Edit billing-dump" }).click();
    const dlg = page.getByRole("dialog", { name: "Edit validator" });
    await expect(dlg.getByLabel("Name", { exact: true })).toHaveValue("billing-dump");
    await expect(dlg.getByLabel("Name", { exact: true })).toHaveAttribute("readonly", "");
    await expect(dlg.getByLabel("Artifact-name pattern")).toHaveValue(BILLING);
    await expect(dlg.getByLabel("Applies to")).toHaveValue("production/billing-mysql");
    await expect(dlg.getByLabel("Script", { exact: true })).toHaveValue("");
    await expect(dlg).toContainText("Leave the script empty to keep the one that is uploaded");
    const next = String.raw`^billing-\d{8}\.sql\.(gz|zst)$`;
    await dlg.getByLabel("Artifact-name pattern").fill(next);
    // Editing a validator is not a collision with itself.
    await expect(dlg.locator("[data-validator-preview]")).not.toContainText("two validators");
    await dlg.getByRole("button", { name: "Save changes" }).click();
    await expect(dlg).toHaveCount(0);
    await expect(card(page).getByRole("row").filter({ hasText: "billing-dump" })).toContainText(next);
    await expect(card(page).locator("[data-validators-status]")).toContainText("Saved billing-dump");
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the dialog meets WCAG AA in ${theme}`, async ({ page }) => {
      const dlg = await openAdd(page);
      await dlg.getByRole("radio", { name: POSTGRES }).check();
      // Everything filled in, so the primary button is live and measured too.
      await dlg.getByLabel("Name", { exact: true }).fill("postgres-dump");
      await dlg.getByRole("button", { name: "Start from the default script" }).click();
      await expect(dlg.getByRole("button", { name: "Upload validator" })).toBeEnabled();
      await setTheme(page, theme);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      expect(await measureContrast(page, "1203 add dialog " + theme, '[role="dialog"]'), theme).toEqual([]);
      expect(await measureBorders(page, "1203 add dialog " + theme, '[role="dialog"] input, [role="dialog"] select, [role="dialog"] textarea, [role="dialog"] button'), theme).toEqual([]);
      await dlg.getByLabel("Artifact-name pattern").fill("^(?=x)$");
      await expect(dlg.locator("[data-validator-preview]").getByRole("alert")).toBeVisible();
      expect(await measureContrast(page, "1203 add dialog refused pattern " + theme, "[data-validator-preview]"), theme).toEqual([]);
    });
  }
});
