import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureBorders, measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1189: the Workflow card on a backup set page said hook directories
 * are set "per set here" and offered nothing to set. Edit mode now carries a
 * before and an after workflow directory, written through the same PATCH
 * `retnd backup-set workflow patch --before-dir/--after-dir` uses.
 *
 * "workflow-no-globals" is the deployment the issue describes: a workflow
 * root, no deployment-wide stages, and a set (media/weekly-archive) with no
 * block of its own.
 */
const QUIET = "/sets/media/weekly-archive";
const CONFIGURED = "/sets/production/postgres-primary";
const SCENARIO = "workflow-no-globals";
const card = (page: Page) => page.locator("section.card").filter({ has: page.locator("h2.eyebrow", { hasText: /^Workflow$/ }) });
const BROKEN = "/srv/hooks/known-broken";

async function edit(page: Page, path = QUIET, scenario = SCENARIO) {
  await open(page, path, scenario);
  await expect(card(page)).toBeVisible();
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await expect(page.getByRole("button", { name: /SAVE ALL/ })).toBeVisible();
}

test.describe("#1189 set workflow hooks in edit mode", () => {
  test("outside edit mode the card points at edit mode, and does not claim a control that is not there", async ({ page }) => {
    await open(page, QUIET, SCENARIO);
    const text = card(page);
    await expect(text).toContainText("No hooks are configured for this backup set");
    await expect(text).not.toContainText("per set here");
    await expect(text).toContainText("edit mode");
    await expect(page.getByTestId("workflow-set-before-input")).toHaveCount(0);
  });

  test("edit mode shows before and after fields on a set with no hooks", async ({ page }) => {
    await edit(page);
    await expect(page.getByTestId("workflow-set-before-input")).toBeVisible();
    await expect(page.getByTestId("workflow-set-after-input")).toBeVisible();
    await expect(page.getByTestId("workflow-set-before-input")).toBeEnabled();
    await expect(page.getByTestId("workflow-set-effective")).toContainText("Nothing runs before this set");
  });

  test("saving a before directory lists it as an effective hook", async ({ page }) => {
    await edit(page);
    await page.getByTestId("workflow-set-before-input").fill("weekly-archive/before");
    await page.getByTestId("workflow-set-before-save").click();
    const eff = page.getByTestId("workflow-set-effective");
    await expect(eff).toContainText("weekly-archive/before");
    await expect(eff).toContainText("this set");
    await expect(page.getByTestId("workflow-set-before-input")).toHaveValue("weekly-archive/before");
    // leaving edit mode, the read-only cells show it too
    await page.getByRole("button", { name: /SAVE ALL/ }).click();
    await expect(card(page)).toContainText("weekly-archive/before");
  });

  test("saving an after directory works independently of the before one", async ({ page }) => {
    await edit(page);
    await page.getByTestId("workflow-set-after-input").fill("weekly-archive/after");
    await page.getByTestId("workflow-set-after-save").click();
    await expect(page.getByTestId("workflow-set-effective")).toContainText("weekly-archive/after");
    await expect(page.getByTestId("workflow-set-before-input")).toHaveValue("");
  });

  test("clearing a saved directory removes it from the effective hooks", async ({ page }) => {
    await edit(page, CONFIGURED, "default");
    await expect(page.getByTestId("workflow-set-before-input")).toHaveValue("/srv/hooks/postgres-primary/before");
    await page.getByTestId("workflow-set-before-input").fill("");
    await page.getByTestId("workflow-set-before-save").click();
    await expect(page.getByTestId("workflow-set-effective")).not.toContainText("/srv/hooks/postgres-primary/before");
    // the deployment's own stage still runs, and the card says so
    await expect(page.getByTestId("workflow-set-effective")).toContainText("/etc/retnd/workflows/before");
  });

  test("effective hooks show the deployment stages beside the set's, in run order", async ({ page }) => {
    await edit(page, CONFIGURED, "default");
    const eff = page.getByTestId("workflow-set-effective");
    await expect(eff).toContainText("/etc/retnd/workflows/before");
    await expect(eff).toContainText("/srv/hooks/postgres-primary/before");
    await expect(eff).toContainText("deployment");
  });

  test("a directory whose script is refused is not saved, and the refusal is shown", async ({ page }) => {
    await edit(page);
    await page.getByTestId("workflow-set-before-input").fill(BROKEN);
    await page.getByTestId("workflow-set-before-save").click();
    await expect(card(page)).toContainText("NOT saved");
    await expect(page.getByTestId("workflow-set-effective")).not.toContainText(BROKEN);
  });

  test("Save stays off until the field differs from what is saved", async ({ page }) => {
    await edit(page);
    await expect(page.getByTestId("workflow-set-before-save")).toBeDisabled();
    await page.getByTestId("workflow-set-before-input").fill("x");
    await expect(page.getByTestId("workflow-set-before-save")).toBeEnabled();
    await page.getByTestId("workflow-set-before-input").fill("");
    await expect(page.getByTestId("workflow-set-before-save")).toBeDisabled();
  });

  test("SAVE ALL saves a typed directory nobody pressed its own Save for", async ({ page }) => {
    await edit(page);
    await page.getByTestId("workflow-set-after-input").fill("weekly-archive/after");
    await page.getByRole("button", { name: /SAVE ALL/ }).click();
    await expect(page.getByRole("button", { name: "Edit", exact: true })).toBeVisible();
    await expect(card(page)).toContainText("weekly-archive/after");
  });

  test("a refused directory keeps SAVE ALL in edit mode with the reason on screen", async ({ page }) => {
    await edit(page);
    await page.getByTestId("workflow-set-before-input").fill(BROKEN);
    await page.getByRole("button", { name: /SAVE ALL/ }).click();
    await expect(card(page)).toContainText("NOT saved");
    await expect(page.getByRole("button", { name: /SAVE ALL/ })).toBeVisible();
    await expect(page.getByTestId("workflow-set-before-input")).toHaveValue(BROKEN);
  });

  test("CANCEL drops typed text, and the next Edit starts from what is saved", async ({ page }) => {
    await edit(page);
    await page.getByTestId("workflow-set-before-input").fill("weekly-archive/before");
    await page.getByRole("button", { name: /CANCEL & EXIT EDIT MODE/ }).click();
    await expect(page.getByRole("button", { name: "Edit", exact: true })).toBeVisible();
    await expect(card(page)).not.toContainText("weekly-archive/before");
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await expect(page.getByTestId("workflow-set-before-input")).toHaveValue("");
  });

  test("with the destructive gate shut the fields still save, because this write is not behind it", async ({ page }) => {
    await edit(page, CONFIGURED, "gate-shut");
    await expect(page.getByTestId("workflow-set-after-input")).toBeEnabled();
    await page.getByTestId("workflow-set-after-input").fill("/srv/hooks/postgres-primary/after-2");
    await page.getByTestId("workflow-set-after-save").click();
    await expect(page.getByTestId("workflow-set-effective")).toContainText("/srv/hooks/postgres-primary/after-2");
  });

  test("a read-only deployment offers no way into the fields", async ({ page }) => {
    await open(page, QUIET, "version-mismatch");
    await expect(card(page)).toBeVisible();
    await expect(page.getByRole("button", { name: "Edit", exact: true })).toBeDisabled();
    await expect(page.getByTestId("workflow-set-before-input")).toHaveCount(0);
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the new controls meet WCAG AA in the ${theme} theme`, async ({ page }) => {
      await edit(page);
      await page.getByTestId("workflow-set-before-input").fill("weekly-archive/before");
      await page.getByTestId("workflow-set-before-save").click();
      await expect(page.getByTestId("workflow-set-effective")).toContainText("weekly-archive/before");
      // An unsaved after directory, so its Save is enabled and measured too.
      await page.getByTestId("workflow-set-after-input").fill("weekly-archive/after");
      await setTheme(page, theme);
      const editor = '[data-testid="workflow-set-editor"]';
      expect(await measureContrast(page, `#1189 hooks editor, ${theme}`, editor)).toEqual([]);
      expect(await measureBorders(page, `#1189 hooks editor, ${theme}`, `${editor} input, ${editor} button`)).toEqual([]);
    });
  }
});
