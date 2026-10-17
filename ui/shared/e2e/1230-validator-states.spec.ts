/**
 * Issue #1230: a custom validator's script state (pinned, not pinned yet,
 * edited on disk, missing) on the three screens that name a validator: the
 * Settings card, the edit dialog and a backup's own "Declare validator" line.
 *
 * Every state is said in words, not just colour, and the two failure states
 * say what to do. The scenarios are the mock's: "validators-states" lists all
 * four at once (billing-dump pinned, any-tar not pinned yet, manifest-check
 * edited on disk, restore-log missing), and "validators-state-unpinned",
 * "-edited" and "-missing" put billing-dump itself in that state so a
 * backup's page can show it. The default scenario is all pinned.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

const BILLING_PAGE = "/backups/production/billing-mysql/billing-20260827.sql.gz";
const card = (p: Page) => p.getByRole("region", { name: "Recovery-test validators" });
const rowOf = (p: Page, name: string) => card(p).getByRole("row").filter({ hasText: name });
const stateOf = (p: Page, name: string) => rowOf(p, name).locator("[data-script-state]");
const assurance = (p: Page) => p.getByRole("region", { name: "Assurance" });
const editDialog = (p: Page) => p.getByRole("dialog", { name: "Edit validator" });

const EDITED = "Edited on disk since you uploaded it";
const UNPINNED = "Not pinned yet (saving it pins it)";
const MISSING = "Script missing";

test.describe("#1230 Settings card: the script state on each row", () => {
  test("says each of the four states in words, with the state as an attribute", async ({ page }) => {
    await open(page, "/settings", "validators-states");
    await expect(card(page)).toBeVisible();
    await expect(stateOf(page, "billing-dump")).toHaveAttribute("data-script-state", "pinned");
    await expect(stateOf(page, "billing-dump")).toContainText("Pinned");
    await expect(stateOf(page, "any-tar")).toHaveAttribute("data-script-state", "unpinned");
    await expect(stateOf(page, "any-tar")).toContainText(UNPINNED);
    await expect(stateOf(page, "manifest-check")).toHaveAttribute("data-script-state", "edited_on_disk");
    await expect(stateOf(page, "manifest-check")).toContainText(EDITED);
    await expect(stateOf(page, "restore-log")).toHaveAttribute("data-script-state", "missing");
    await expect(stateOf(page, "restore-log")).toContainText(MISSING);
  });

  test("the default scenario shows every validator pinned", async ({ page }) => {
    await open(page, "/settings");
    await expect(stateOf(page, "billing-dump")).toContainText("Pinned");
    await expect(stateOf(page, "any-tar")).toContainText("Pinned");
    await expect(card(page)).not.toContainText(EDITED);
    await expect(card(page)).not.toContainText(MISSING);
  });

  test("a pinned row names the hash that is pinned, and a failure row names what to do", async ({ page }) => {
    await open(page, "/settings", "validators-states");
    await expect(stateOf(page, "billing-dump")).toContainText(/sha256 [0-9a-f]{12}\b/);
    const edited = stateOf(page, "manifest-check");
    await expect(edited).toContainText("could not complete");
    await expect(edited).toContainText("approve it");
    await expect(edited).toContainText("Edit");
    const missing = stateOf(page, "restore-log");
    await expect(missing).toContainText("cannot complete");
    await expect(missing).toContainText("Restore");
    await expect(missing).toContainText("upload");
  });

  test("not pinned yet says it still runs and that saving pins it", async ({ page }) => {
    await open(page, "/settings", "validators-states");
    await expect(stateOf(page, "any-tar")).toContainText("runs as it did");
  });

  test("the failure states look different from the healthy ones, and are bold as well as coloured", async ({ page }) => {
    await open(page, "/settings", "validators-states");
    const look = (name: string) =>
      stateOf(page, name).locator("[data-script-state-label]").evaluate((el) => {
        const s = getComputedStyle(el);
        return { color: s.color, weight: Number(s.fontWeight) };
      });
    await expect(stateOf(page, "manifest-check")).toBeVisible();
    const healthy = await look("billing-dump");
    for (const failing of ["manifest-check", "restore-log"]) {
      const f = await look(failing);
      expect(f.color, failing).not.toBe(healthy.color);
      expect(f.weight, failing).toBeGreaterThan(healthy.weight);
    }
  });

  test("a failure row is announced to a screen reader as a status, not left to colour", async ({ page }) => {
    await open(page, "/settings", "validators-states");
    await expect(rowOf(page, "manifest-check").getByRole("status")).toContainText(EDITED);
    await expect(rowOf(page, "restore-log").getByRole("status")).toContainText(MISSING);
  });

  test("agrees with the CLI's list output on what each state means", async ({ page }) => {
    // core/cmd/retnd/assurancevalidators.go prints: "not pinned in config.yaml ...
    // it runs as it did, and saving the validator again pins it", "EDITED ON DISK
    // ... ends could_not_complete without running it ... to approve it" and
    // "MISSING, so a test it applies to cannot complete".
    await open(page, "/settings", "validators-states");
    const text = async (name: string) => ((await stateOf(page, name).textContent()) ?? "").toLowerCase();
    await expect(stateOf(page, "any-tar")).toBeVisible();
    const unpinned = await text("any-tar");
    for (const w of ["not pinned", "runs as it did", "pins it"]) expect(unpinned).toContain(w);
    const edited = await text("manifest-check");
    for (const w of ["edited on disk", "could not complete", "without running it", "approve it"]) expect(edited).toContain(w);
    const missing = await text("restore-log");
    for (const w of ["missing", "cannot complete"]) expect(missing).toContain(w);
  });
});

test.describe("#1230 edit dialog", () => {
  test("an edited-on-disk script cannot be kept: the dialog says why and Save waits for a script", async ({ page }) => {
    await open(page, "/settings", "validators-states");
    await card(page).getByRole("button", { name: "Edit manifest-check" }).click();
    const dlg = editDialog(page);
    const note = dlg.locator("[data-validator-script-state]");
    await expect(note).toContainText(EDITED);
    await expect(note).toContainText("no longer matches");
    await expect(note).toContainText("VALIDATOR_SCRIPT_CHANGED");
    await expect(note).toContainText("approve");
    await expect(note).toContainText(/pinned [0-9a-f]{12}/);
    await expect(note).toContainText(/on disk [0-9a-f]{12}/);
    await expect(dlg).not.toContainText("Leave the script empty to keep");
    await expect(dlg.locator("#validator-script-help")).toContainText("cannot be kept");
    await expect(dlg.getByRole("button", { name: "Save changes" })).toBeDisabled();
  });

  test("sending the script approves it, and the row reads Pinned afterwards", async ({ page }) => {
    await open(page, "/settings", "validators-states");
    await card(page).getByRole("button", { name: "Edit manifest-check" }).click();
    const dlg = editDialog(page);
    await dlg.getByRole("button", { name: "Start from the default script" }).click();
    await expect(dlg.getByRole("button", { name: "Save changes" })).toBeEnabled();
    await dlg.getByRole("button", { name: "Save changes" }).click();
    await expect(editDialog(page)).toHaveCount(0);
    await expect(card(page).locator("[data-validators-status]")).toContainText("Saved manifest-check");
    await expect(stateOf(page, "manifest-check")).toHaveAttribute("data-script-state", "pinned");
    await expect(stateOf(page, "manifest-check")).not.toContainText(EDITED);
  });

  test("a missing script cannot be kept either, and says to restore it or upload it again", async ({ page }) => {
    await open(page, "/settings", "validators-states");
    await card(page).getByRole("button", { name: "Edit restore-log" }).click();
    const dlg = editDialog(page);
    const note = dlg.locator("[data-validator-script-state]");
    await expect(note).toContainText(MISSING);
    await expect(note).toContainText("Restore");
    await expect(dlg.locator("#validator-script-help")).toContainText("cannot be kept");
    await expect(dlg.getByRole("button", { name: "Save changes" })).toBeDisabled();
    await dlg.getByRole("button", { name: "Start from the default script" }).click();
    await dlg.getByRole("button", { name: "Save changes" }).click();
    await expect(stateOf(page, "restore-log")).toHaveAttribute("data-script-state", "pinned");
  });

  test("a validator not pinned yet can still be kept, and saving it pins it", async ({ page }) => {
    await open(page, "/settings", "validators-states");
    await card(page).getByRole("button", { name: "Edit any-tar" }).click();
    const dlg = editDialog(page);
    await expect(dlg.locator("[data-validator-script-state]")).toContainText("saving it pins it");
    await expect(dlg.locator("#validator-script-help")).toContainText("Leave the script empty to keep");
    await expect(dlg.getByRole("button", { name: "Save changes" })).toBeEnabled();
    await dlg.getByRole("button", { name: "Save changes" }).click();
    await expect(editDialog(page)).toHaveCount(0);
    await expect(stateOf(page, "any-tar")).toHaveAttribute("data-script-state", "pinned");
  });

  test("a pinned validator keeps its script as before, and names the pin", async ({ page }) => {
    await open(page, "/settings", "validators-states");
    await card(page).getByRole("button", { name: "Edit billing-dump" }).click();
    const dlg = editDialog(page);
    await expect(dlg.locator("[data-validator-script-state]")).toContainText("Pinned");
    await expect(dlg.locator("#validator-script-help")).toContainText("Leave the script empty to keep");
    await expect(dlg.getByRole("button", { name: "Save changes" })).toBeEnabled();
  });

  test("adding a validator shows no script state", async ({ page }) => {
    await open(page, "/settings", "validators-states");
    await card(page).getByRole("button", { name: "Add validator" }).click();
    await expect(page.getByRole("dialog", { name: "Add validator" }).locator("[data-validator-script-state]")).toHaveCount(0);
  });
});

test.describe("#1230 backup page, Declare validator line", () => {
  const line = (p: Page) => assurance(p).locator("[data-validator-script-state]");

  test("a pinned validator that would run says Pinned beside its name", async ({ page }) => {
    await open(page, BILLING_PAGE);
    await expect(assurance(page).locator("[data-validator-covered]")).toContainText("billing-dump");
    await expect(line(page)).toHaveAttribute("data-script-state", "pinned");
    await expect(line(page)).toContainText("Pinned");
  });

  test("a validator not pinned yet says so", async ({ page }) => {
    await open(page, BILLING_PAGE, "validators-state-unpinned");
    await expect(line(page)).toHaveAttribute("data-script-state", "unpinned");
    await expect(line(page)).toContainText(UNPINNED);
  });

  test("an edited-on-disk validator says the test will not complete and what to do", async ({ page }) => {
    await open(page, BILLING_PAGE, "validators-state-edited");
    await expect(line(page)).toHaveAttribute("data-script-state", "edited_on_disk");
    await expect(line(page)).toContainText(EDITED);
    await expect(line(page)).toContainText("could not complete");
    await expect(line(page)).toContainText("Settings");
    await expect(assurance(page).locator("[data-validator-covered]")).toContainText("billing-dump");
  });

  test("a validator whose script is missing says so", async ({ page }) => {
    await open(page, BILLING_PAGE, "validators-state-missing");
    await expect(line(page)).toHaveAttribute("data-script-state", "missing");
    await expect(line(page)).toContainText(MISSING);
    await expect(line(page)).toContainText("Restore");
  });

  test("a backup no validator covers shows no script state", async ({ page }) => {
    await open(page, "/backups/production/postgres-primary/postgres-prod-20260828.dump.zst", "validators-state-edited");
    await expect(assurance(page).getByRole("button", { name: "Declare validator" })).toBeEnabled();
    await expect(line(page)).toHaveCount(0);
  });
});

for (const theme of ["light", "dark"] as const) {
  test.describe(`#1230 contrast in ${theme}`, () => {
    test(`the card's four states meet WCAG AA in ${theme}`, async ({ page }) => {
      await open(page, "/settings", "validators-states");
      await expect(stateOf(page, "restore-log")).toBeVisible();
      // After the app has booted: its own start-up sets the theme attribute.
      await setTheme(page, theme);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      expect(await measureContrast(page, "1230 settings card states " + theme, "[data-script-state]"), theme).toEqual([]);
      expect(await measureContrast(page, "1230 settings card " + theme, '[aria-label="Recovery-test validators"]'), theme).toEqual([]);
    });

    test(`the edit dialog for an edited script meets WCAG AA in ${theme}`, async ({ page }) => {
      await open(page, "/settings", "validators-states");
      await expect(stateOf(page, "manifest-check")).toBeVisible();
      await setTheme(page, theme);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      await card(page).getByRole("button", { name: "Edit manifest-check" }).click();
      await expect(editDialog(page).locator("[data-validator-script-state]")).toBeVisible();
      expect(await measureContrast(page, "1230 edit dialog note " + theme, "[data-validator-dialog] [data-validator-script-state]"), theme).toEqual([]);
      expect(await measureContrast(page, "1230 edit dialog " + theme, '[role="dialog"]'), theme).toEqual([]);
    });

    test(`the Declare validator line for an edited script meets WCAG AA in ${theme}`, async ({ page }) => {
      await open(page, BILLING_PAGE, "validators-state-edited");
      await expect(assurance(page).locator("[data-validator-script-state]")).toBeVisible();
      await setTheme(page, theme);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      expect(await measureContrast(page, "1230 declare line " + theme, "[data-validator-covered], [data-validator-script-state]"), theme).toEqual([]);
    });
  });
}
