/**
 * Issue #1233: a custom validator's environment (minimal or workflow) on the
 * three screens that name a validator: the Settings card, the add and edit
 * dialog and a backup's own "Declare validator" line.
 *
 * The words are `retnd assurance validators list`'s own: it prints "env
 * minimal" or "env workflow" (core/cmd/retnd/assurancevalidators.go), and the
 * page says "Minimal environment" or "Workflow environment, secrets included".
 * Scenarios: "validators-env-workflow" has billing-dump on the workflow
 * environment and any-tar on the minimal one; every other scenario is minimal.
 */
import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

const BILLING_PAGE = "/backups/production/billing-mysql/billing-20260827.sql.gz";
const POSTGRES = String.raw`^postgres-prod-\d{8}\.dump\.zst$`;
const PASSWORD = "correct-horse-battery";
const MINIMAL = "Minimal environment";
const WORKFLOW = "Workflow environment, secrets included";

const card = (p: Page) => p.getByRole("region", { name: "Recovery-test validators" });
const rowOf = (p: Page, name: string) => card(p).getByRole("row").filter({ hasText: name });
const envOf = (p: Page, name: string) => rowOf(p, name).locator("[data-validator-env]");
const assurance = (p: Page) => p.getByRole("region", { name: "Assurance" });
const addDialog = (p: Page) => p.getByRole("dialog", { name: "Add validator" });
const editDialog = (p: Page) => p.getByRole("dialog", { name: "Edit validator" });
const choice = (d: Locator) => d.getByRole("radiogroup", { name: "Environment" });

async function filledAdd(page: Page, scenario: string) {
  await open(page, "/settings", scenario);
  await card(page).getByRole("button", { name: "Add validator" }).click();
  const dlg = addDialog(page);
  await dlg.getByLabel("Name", { exact: true }).fill("postgres-dump");
  await dlg.getByRole("radio", { name: POSTGRES }).check();
  await dlg.getByRole("button", { name: "Start from the default script" }).click();
  return dlg;
}

test.describe("#1233 Settings card: each validator's environment", () => {
  test("says each environment in words, with the CLI's word as an attribute", async ({ page }) => {
    await open(page, "/settings", "validators-env-workflow");
    await expect(card(page)).toBeVisible();
    await expect(envOf(page, "billing-dump")).toHaveAttribute("data-validator-env", "workflow");
    await expect(envOf(page, "billing-dump")).toContainText(WORKFLOW);
    await expect(envOf(page, "any-tar")).toHaveAttribute("data-validator-env", "minimal");
    await expect(envOf(page, "any-tar")).toContainText(MINIMAL);
  });

  test("the default scenario shows every validator on the minimal environment", async ({ page }) => {
    await open(page, "/settings");
    await expect(envOf(page, "billing-dump")).toContainText(MINIMAL);
    await expect(envOf(page, "any-tar")).toContainText(MINIMAL);
    await expect(card(page)).not.toContainText(WORKFLOW);
  });

  test("the workflow one reads as the one needing care: bold and set apart, not only a colour", async ({ page }) => {
    await open(page, "/settings", "validators-env-workflow");
    await expect(envOf(page, "billing-dump")).toBeVisible();
    const look = (name: string) =>
      envOf(page, name).evaluate((el) => {
        const s = getComputedStyle(el);
        return { weight: Number(s.fontWeight), background: s.backgroundColor, text: el.textContent ?? "" };
      });
    const minimal = await look("any-tar");
    const workflow = await look("billing-dump");
    expect(workflow.weight).toBeGreaterThan(minimal.weight);
    expect(workflow.background).not.toBe(minimal.background);
    expect(workflow.text).toMatch(/secrets/);
  });

  test("agrees with the CLI's list output: the same two words", async ({ page }) => {
    // The CLI prints "  env minimal" and "  env workflow" for each validator.
    await open(page, "/settings", "validators-env-workflow");
    for (const [name, cli] of [["billing-dump", "workflow"], ["any-tar", "minimal"]] as const) {
      await expect(envOf(page, name)).toHaveAttribute("data-validator-env", cli);
      expect(((await envOf(page, name).textContent()) ?? "").toLowerCase()).toContain(cli);
    }
  });
});

test.describe("#1233 add and edit dialog: the choice", () => {
  test("an add offers both environments, minimal chosen, and says what workflow exposes", async ({ page }) => {
    await open(page, "/settings");
    await card(page).getByRole("button", { name: "Add validator" }).click();
    const dlg = addDialog(page);
    await expect(choice(dlg)).toBeVisible();
    await expect(choice(dlg).getByRole("radio", { name: MINIMAL })).toBeChecked();
    await expect(choice(dlg).getByRole("radio", { name: WORKFLOW })).not.toBeChecked();
    await expect(choice(dlg)).toContainText("workflow variables");
    await expect(choice(dlg)).toContainText("secrets");
  });

  test("an add that chooses workflow lands on the card as workflow", async ({ page }) => {
    const dlg = await filledAdd(page, "default");
    await choice(dlg).getByRole("radio", { name: WORKFLOW }).check();
    await dlg.getByRole("button", { name: "Upload validator" }).click();
    await expect(addDialog(page)).toHaveCount(0);
    await expect(envOf(page, "postgres-dump")).toHaveAttribute("data-validator-env", "workflow");
    await expect(envOf(page, "postgres-dump")).toContainText(WORKFLOW);
  });

  test("an add that leaves the choice alone lands on the card as minimal", async ({ page }) => {
    const dlg = await filledAdd(page, "default");
    await dlg.getByRole("button", { name: "Upload validator" }).click();
    await expect(addDialog(page)).toHaveCount(0);
    await expect(envOf(page, "postgres-dump")).toHaveAttribute("data-validator-env", "minimal");
  });

  test("an edit starts on the validator's own environment and can move it either way", async ({ page }) => {
    await open(page, "/settings", "validators-env-workflow");
    await card(page).getByRole("button", { name: "Edit billing-dump" }).click();
    let dlg = editDialog(page);
    await expect(choice(dlg).getByRole("radio", { name: WORKFLOW })).toBeChecked();
    await choice(dlg).getByRole("radio", { name: MINIMAL }).check();
    await dlg.getByRole("button", { name: "Save changes" }).click();
    await expect(editDialog(page)).toHaveCount(0);
    await expect(envOf(page, "billing-dump")).toHaveAttribute("data-validator-env", "minimal");

    await card(page).getByRole("button", { name: "Edit any-tar" }).click();
    dlg = editDialog(page);
    await expect(choice(dlg).getByRole("radio", { name: MINIMAL })).toBeChecked();
    await choice(dlg).getByRole("radio", { name: WORKFLOW }).check();
    await dlg.getByRole("button", { name: "Save changes" }).click();
    await expect(editDialog(page)).toHaveCount(0);
    await expect(envOf(page, "any-tar")).toHaveAttribute("data-validator-env", "workflow");
  });

  test("an edit that changes nothing about the environment keeps it", async ({ page }) => {
    await open(page, "/settings", "validators-env-workflow");
    await card(page).getByRole("button", { name: "Edit billing-dump" }).click();
    await editDialog(page).getByRole("button", { name: "Save changes" }).click();
    await expect(editDialog(page)).toHaveCount(0);
    await expect(envOf(page, "billing-dump")).toHaveAttribute("data-validator-env", "workflow");
  });
});

test.describe("#1233 choosing workflow takes the gate and the password an upload takes", () => {
  test("a shut gate is said before anything is pressed once workflow is chosen", async ({ page }) => {
    const dlg = await filledAdd(page, "gateway-profile");
    await choice(dlg).getByRole("radio", { name: WORKFLOW }).check();
    await expect(dlg.locator("[data-gate-notice]")).toBeVisible();
    await expect(dlg.locator("[data-env-gate]")).toContainText("same gate");
    await expect(dlg.locator("[data-env-gate]")).toContainText("password");
    // The command for the host carries the choice, so running it does what was asked.
    await expect(dlg.locator("[data-gate-notice]")).toContainText("--env workflow");
  });

  test("pressing upload with workflow on a shut gate is refused and nothing is added", async ({ page }) => {
    const dlg = await filledAdd(page, "gate-shut");
    await choice(dlg).getByRole("radio", { name: WORKFLOW }).check();
    await dlg.getByRole("button", { name: "Upload validator" }).click();
    await expect(dlg.locator("[data-gate-refusal]")).toContainText("The validator was not uploaded");
    await expect(rowOf(page, "postgres-dump")).toHaveCount(0);
  });

  test("a profile that re-checks the password asks for it before workflow is saved", async ({ page }) => {
    const dlg = await filledAdd(page, "local-account-profile");
    await choice(dlg).getByRole("radio", { name: WORKFLOW }).check();
    const upload = dlg.getByRole("button", { name: "Upload validator" });
    await expect(upload).toBeDisabled();
    await dlg.getByLabel("Your password").fill(PASSWORD);
    await expect(upload).toBeEnabled();
    await upload.click();
    await expect(addDialog(page)).toHaveCount(0);
    await expect(envOf(page, "postgres-dump")).toHaveAttribute("data-validator-env", "workflow");
  });

  test("moving a validator to workflow by an edit takes the password too, and a wrong one changes nothing", async ({ page }) => {
    await open(page, "/settings", "local-account-profile");
    await card(page).getByRole("button", { name: "Edit any-tar" }).click();
    const dlg = editDialog(page);
    await choice(dlg).getByRole("radio", { name: WORKFLOW }).check();
    const save = dlg.getByRole("button", { name: "Save changes" });
    await expect(save).toBeDisabled();
    await dlg.getByLabel("Your password").fill("not-the-password");
    await save.click();
    await expect(dlg.locator("[data-validator-error]")).toContainText("That password was not accepted");
    await dlg.getByRole("button", { name: "Cancel" }).click();
    await expect(envOf(page, "any-tar")).toHaveAttribute("data-validator-env", "minimal");
  });

  test("the sentence about the gate is not shown while minimal is chosen", async ({ page }) => {
    const dlg = await filledAdd(page, "gateway-profile");
    await expect(dlg.locator("[data-env-gate]")).toHaveCount(0);
  });
});

test.describe("#1233 the Declare validator line", () => {
  test("names the environment of the validator that would run: workflow", async ({ page }) => {
    await open(page, BILLING_PAGE, "validators-env-workflow");
    const line = assurance(page).locator("[data-validator-env]");
    await expect(line).toHaveAttribute("data-validator-env", "workflow");
    await expect(line).toContainText(WORKFLOW);
    await expect(assurance(page).locator("[data-validator-env-line]")).toContainText("billing-dump");
  });

  test("names the environment of the validator that would run: minimal", async ({ page }) => {
    await open(page, BILLING_PAGE);
    const line = assurance(page).locator("[data-validator-env]");
    await expect(line).toHaveAttribute("data-validator-env", "minimal");
    await expect(line).toContainText(MINIMAL);
  });

  test("a backup no validator covers shows no environment", async ({ page }) => {
    await open(page, "/backups/production/postgres-primary/postgres-prod-20260828.dump.zst", "validators-env-workflow");
    await expect(assurance(page).getByRole("button", { name: "Declare validator" })).toBeEnabled();
    await expect(assurance(page).locator("[data-validator-env]")).toHaveCount(0);
  });
});

for (const theme of ["light", "dark"] as const) {
  test.describe(`#1233 contrast in ${theme}`, () => {
    test(`the card's environments meet WCAG AA in ${theme}`, async ({ page }) => {
      await open(page, "/settings", "validators-env-workflow");
      await expect(envOf(page, "billing-dump")).toBeVisible();
      // After the app has booted: its own start-up sets the theme attribute.
      await setTheme(page, theme);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      expect(await measureContrast(page, "1233 card environments " + theme, "[data-validator-env]"), theme).toEqual([]);
      expect(await measureContrast(page, "1233 card " + theme, '[aria-label="Recovery-test validators"]'), theme).toEqual([]);
    });

    test(`the dialog's choice meets WCAG AA in ${theme}`, async ({ page }) => {
      await open(page, "/settings", "validators-env-workflow");
      await expect(envOf(page, "billing-dump")).toBeVisible();
      await setTheme(page, theme);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      await card(page).getByRole("button", { name: "Edit billing-dump" }).click();
      await expect(choice(editDialog(page))).toBeVisible();
      expect(await measureContrast(page, "1233 dialog choice " + theme, "[data-validator-env-choice]"), theme).toEqual([]);
      expect(await measureContrast(page, "1233 dialog " + theme, '[role="dialog"]'), theme).toEqual([]);
    });

    test(`the dialog's gate sentence meets WCAG AA in ${theme}`, async ({ page }) => {
      const dlg = await filledAdd(page, "gateway-profile");
      await choice(dlg).getByRole("radio", { name: WORKFLOW }).check();
      await expect(dlg.locator("[data-env-gate]")).toBeVisible();
      await setTheme(page, theme);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      expect(await measureContrast(page, "1233 env gate sentence " + theme, "[data-env-gate]"), theme).toEqual([]);
    });

    test(`the Declare validator line meets WCAG AA in ${theme}`, async ({ page }) => {
      await open(page, BILLING_PAGE, "validators-env-workflow");
      await expect(assurance(page).locator("[data-validator-env]")).toBeVisible();
      await setTheme(page, theme);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      expect(await measureContrast(page, "1233 declare line " + theme, "[data-validator-covered], [data-validator-env]"), theme).toEqual([]);
    });
  });
}
