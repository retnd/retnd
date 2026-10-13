import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureBorders, measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1111 (the chip half): a set with no validator and a .tar.zst newest
 * copy. Pressing Run recovery test runs the built-in structure check; the end
 * line reads "recovered and readable" and the Recovery chip reads Recovered,
 * readable, never Recovery test passed. Driven with `wire=1`, so realAssurance
 * (the code a built app runs) reads the mock's routes.
 */
const SET = "/sets/production/postgres-primary";
const region = (page: Page) => page.getByRole("region", { name: "Assurance" });
const terminal = (page: Page) => page.getByLabel("Terminal", { exact: true }).getByRole("log");

async function runToTheEnd(page: Page) {
  await open(page, SET, "assurance-builtin-check", { wire: true });
  const section = region(page);
  const recovery = section.locator('[data-claim="recovery"]');
  // mode is off, so before the first press the chip is "Not configured": the
  // built-in check governs the manual test, not the schedule.
  await expect(recovery).toHaveAttribute("data-state", "never_configured");
  const run = section.getByRole("button", { name: "Run recovery test" });
  await expect(run, "the built-in check can run, so the button is offered").toBeEnabled();
  await expect(section.getByText(/no recovery-test validator is declared/i)).toHaveCount(0);
  await run.click();
  await expect(recovery).toHaveAttribute("data-state", "pass_builtin", { timeout: 60_000 });
  return { section, recovery };
}

test.describe("#1111 built-in structure check: the chip and the feed", () => {
  test("the end line says recovered and readable and the Recovery chip says Recovered, readable", async ({ page }) => {
    const { recovery } = await runToTheEnd(page);
    await expect(terminal(page)).toContainText("recovery test started: postgres-prod-20260829.tar.zst (built-in structure check)");
    const end = terminal(page).getByText("recovered and readable: postgres-prod-20260829.tar.zst (built-in check)");
    await expect(end).toBeVisible();
    await expect(terminal(page)).not.toContainText("recovery test passed");
    await expect(recovery).toContainText("Recovered, readable");
    await expect(recovery).not.toContainText("Recovery test passed");
    await expect(recovery.locator('[data-glyph="pass-builtin"]')).toHaveCount(1);
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`#1111 the Recovered, readable chip meets AA in the ${theme} theme`, async ({ page }) => {
    const { recovery } = await runToTheEnd(page);
    await setTheme(page, theme);
    await expect(recovery).toHaveAttribute("data-state", "pass_builtin");
    expect(await measureContrast(page, `pass_builtin chip, ${theme}`, '[data-claim="recovery"]')).toEqual([]);
    expect(await measureBorders(page, `pass_builtin chip, ${theme}`, '[data-claim="recovery"] .achip')).toEqual([]);
    await expect(recovery.locator(".achip")).toHaveCSS("border-top-style", "dashed");
  });
}
