/**
 * Issue #1246, Settings: the recovery-test validators card on a deployment
 * whose config.yaml names no workflow root. The Docker host install mounts
 * <prefix>/workflows at /workflows and wrote nothing into config.yaml, and the
 * card told the operator to go and configure a root the product already had.
 *
 * "validators-default-root" is that deployment once the engine uses the
 * mounted directory: the card says where scripts go and that the root is the
 * default, and offers to add one. "validators-workflows-off" is a root the
 * operator cleared on purpose, which still refuses and says why.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

const card = (p: Page) => p.getByRole("region", { name: "Recovery-test validators" });

test.describe("#1246 Settings, validators on the default workflow root", () => {
  test("a deployment on the mounted /workflows can add a validator", async ({ page }) => {
    await open(page, "/settings", "validators-default-root");
    await expect(card(page)).toBeVisible();
    await expect(card(page)).not.toContainText("no workflow root is configured");
    await expect(card(page)).not.toContainText("cannot keep custom validators");
    await expect(card(page).getByRole("button", { name: "Add validator" })).toBeEnabled();
  });

  test("says where scripts are kept and that the root is the default", async ({ page }) => {
    await open(page, "/settings", "validators-default-root");
    const where = card(page).locator("[data-validators-root]");
    await expect(where).toContainText("/workflows/recovery-test-validators");
    await expect(where).toContainText("the default workflow root");
    await expect(where).not.toContainText("not configured");
  });

  test("a root named in config.yaml is not called the default", async ({ page }) => {
    await open(page, "/settings", "validators-none");
    const where = card(page).locator("[data-validators-root]");
    await expect(where).toContainText("/var/lib/retnd/workflows/recovery-test-validators");
    await expect(where).not.toContainText("default");
  });

  test("a root the operator cleared still refuses, and says it was turned off", async ({ page }) => {
    await open(page, "/settings", "validators-workflows-off");
    const why = card(page).locator("[data-validators-unavailable]");
    await expect(why).toContainText("turned off");
    await expect(why).toContainText("workflows.disabled");
    await expect(card(page).getByRole("button", { name: "Add validator" })).toBeDisabled();
    await expect(card(page).locator("[data-validators-root]")).toHaveCount(0);
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the root line meets WCAG AA in ${theme}`, async ({ page }) => {
      await open(page, "/settings", "validators-default-root");
      await expect(card(page).locator("[data-validators-root]")).toBeVisible();
      await setTheme(page, theme);
      expect(await measureContrast(page, "1246 validators root " + theme, "[data-validators-root]"), theme).toEqual([]);
    });
  }
});
