import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme, spaGo } from "./helpers";

/**
 * Issue #1111 (settings lane): the built-in structure check's controls on the
 * Settings assurance card and in a set's own Assurance policy. The dev build
 * keeps its settings in the page's memory, so "round trip" here is: save,
 * leave the screen, come back, and read what the server (the mock's routes)
 * resolves. Every spec ends on a DOM assertion about the state it reached.
 */
const SET = "/sets/production/postgres-primary";
const card = (page: Page) => page.getByRole("region", { name: "Assurance" });
const BUILTIN = "Built-in check when no validator";
const code = (page: Page, text: string | RegExp) => page.locator("code").filter({ hasText: text });

test.describe("#1111 Settings: Built-in check when no validator", () => {
  test("1111 saves the choice, leaves, comes back and reads it resolved", async ({ page }) => {
    await open(page, "/settings");
    const section = card(page);
    const select = section.getByLabel(BUILTIN);
    await expect(select).toHaveValue("structure");
    await select.selectOption("off");
    await section.getByRole("button", { name: "Save" }).click();
    await expect(section.getByRole("status")).toHaveText("Saved");
    await spaGo(page, "/");
    await spaGo(page, "/settings");
    await expect(card(page).getByLabel(BUILTIN)).toHaveValue("off");
    await expect(card(page).getByLabel("Recovery test").locator("option")).toHaveText(["off", "builtin", "required"]);
  });

  test("1111 CommandEcho prints --recovery-test-builtin-check for the change about to be saved", async ({ page }) => {
    await open(page, "/settings");
    await card(page).getByLabel(BUILTIN).selectOption("off");
    await expect(code(page, "retnd settings assurance patch --recovery-test-builtin-check off")).toBeVisible();
    await card(page).getByLabel(BUILTIN).selectOption("structure");
    // back to what is saved: no pending change, so the echo is the shape again
    await expect(code(page, "retnd settings assurance patch --scrub-cadence <duration>")).toBeVisible();
    await expect(code(page, "--recovery-test-builtin-check")).toHaveCount(0);
  });

  test("1111 builtin mode with the check off is refused in the server's words and Save stays disabled", async ({ page }) => {
    await open(page, "/settings");
    const section = card(page);
    await section.getByLabel("Recovery test", { exact: true }).selectOption("builtin");
    await section.getByLabel(BUILTIN).selectOption("off");
    await expect(section.getByRole("alert")).toContainText(
      "mode builtin runs the built-in structure check, so builtin_check: off refuses it; set builtin_check to structure or choose another mode");
    await expect(section.getByRole("button", { name: "Save" })).toBeDisabled();
    await section.getByLabel(BUILTIN).selectOption("structure");
    await expect(section.getByRole("alert")).toHaveCount(0);
    await expect(section.getByRole("button", { name: "Save" })).toBeEnabled();
  });

  test("1111 a validator id beginning retnd- is refused in the server's words", async ({ page }) => {
    await open(page, "/settings");
    const section = card(page);
    await section.getByLabel("Recovery test", { exact: true }).selectOption("required");
    await section.getByLabel("Validator", { exact: true }).fill("retnd-foo");
    await expect(section.getByRole("alert").filter({ hasText: "ids beginning retnd-" }))
      .toHaveText('"retnd-foo" is not usable: ids beginning retnd- are the product\'s own built-in checks');
    await expect(section.getByRole("button", { name: "Save" })).toBeDisabled();
  });
});

test.describe("#1111 a set's own Assurance policy: Built-in check when no validator", () => {
  const own = async (page: Page) => {
    await open(page, SET);
    await card(page).getByRole("radio", { name: "This set's own policy" }).check();
  };

  test("1111 pins the choice with mode builtin, saves it and reads it back", async ({ page }) => {
    await own(page);
    const section = card(page);
    await section.getByLabel("Recovery test", { exact: true }).selectOption("builtin");
    // a builtin mode runs no validator, so there is no field for one
    await expect(section.getByLabel("Validator", { exact: true })).toHaveCount(0);
    await section.getByRole("button", { name: "Save policy" }).click();
    await expect(section.getByRole("status")).toHaveText("Saved");
    await spaGo(page, "/");
    await spaGo(page, SET);
    await expect(card(page).getByRole("radio", { name: "This set's own policy" })).toBeChecked();
    await expect(card(page).getByLabel("Recovery test", { exact: true })).toHaveValue("builtin");
    await expect(card(page).getByLabel(BUILTIN)).toHaveValue("structure");
  });

  test("1111 CommandEcho prints the set's whole own policy ending on --recovery-test-builtin-check", async ({ page }) => {
    await own(page);
    await card(page).getByLabel(BUILTIN).selectOption("off");
    const line = code(page, "retnd backup-set assurance production/postgres-primary");
    await expect(line).toHaveText(/^retnd backup-set assurance production\/postgres-primary --scrub-cadence 168h .* --recovery-test-builtin-check off$/);
  });

  test("1111 builtin mode with the check off, and a retnd- validator id, are each refused before Save", async ({ page }) => {
    await own(page);
    const section = card(page);
    await section.getByLabel("Recovery test", { exact: true }).selectOption("builtin");
    await section.getByLabel(BUILTIN).selectOption("off");
    await expect(section.getByRole("alert")).toContainText("mode builtin runs the built-in structure check, so builtin_check: off refuses it");
    await expect(section.getByRole("button", { name: "Save policy" })).toBeDisabled();
    await section.getByLabel(BUILTIN).selectOption("structure");
    await section.getByLabel("Recovery test", { exact: true }).selectOption("custom");
    await section.getByLabel("Validator", { exact: true }).fill("retnd-x");
    await expect(section.getByRole("alert").filter({ hasText: "ids beginning retnd-" })).toHaveText('"retnd-x" is not usable: ids beginning retnd- are the product\'s own built-in checks');
    await expect(section.getByRole("button", { name: "Save policy" })).toBeDisabled();
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`1111 contrast (${theme}): the new text and controls meet WCAG AA`, async ({ page }) => {
    await open(page, "/settings");
    await expect(page.locator("main")).toBeVisible();
    await setTheme(page, theme);
    await card(page).getByLabel("Recovery test", { exact: true }).selectOption("builtin");
    await card(page).getByLabel(BUILTIN).selectOption("off");
    await expect(card(page).getByRole("alert")).toBeVisible();
    const settings = await measureContrast(page, `1111 settings card ${theme}`, '[aria-label="Assurance"]');
    await spaGo(page, SET);
    await setTheme(page, theme);
    await card(page).getByRole("radio", { name: "This set's own policy" }).check();
    await card(page).getByLabel("Recovery test", { exact: true }).selectOption("builtin");
    await card(page).getByLabel(BUILTIN).selectOption("off");
    await expect(card(page).getByRole("alert")).toBeVisible();
    const perSet = await measureContrast(page, `1111 set section ${theme}`, '[aria-label="Assurance"]');
    expect([...settings, ...perSet]).toEqual([]);
  });
}
