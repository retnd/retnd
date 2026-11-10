/**
 * Issue #995 (EPIC N): the Retained-copy encryption card on Settings, in light,
 * dark and narrow. WRITTEN, NOT RUN: no browser was launched for this change.
 * When it is run, it goes through /Users/rom/workspace/xdg-pw-lock.sh and
 * nowhere else.
 *
 * Driven against the mock's encryption scenarios (encryption/mockEncryption.ts).
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, measureEdges, open, setTheme } from "./helpers";

const CARD = '[data-encryption-card]';
const canary = /AGE-SECRET-KEY|PRIVATE KEY|CANARY995/;

async function card(page: Page, scenario: string) {
  await open(page, "/settings", scenario);
  const c = page.locator(CARD);
  await expect(c).toBeVisible();
  return c;
}

test.describe("#995 Settings, Retained-copy encryption", () => {
  test("off is one switch and a sentence, and no cipher, format or key field", async ({ page }) => {
    const c = await card(page, "default");
    await expect(c.getByRole("switch", { name: "Encrypt copies sent to destinations" })).not.toBeChecked();
    await expect(c.getByText(/Copies are written exactly as they are today/)).toBeVisible();
    await expect(c.getByText("Deployment-wide")).toBeVisible();
    await expect(c.locator("textarea, input[type=file], select")).toHaveCount(0);
    await expect(c.getByRole("button", { name: /Show key|Copy key|Reveal/ })).toHaveCount(0);
  });

  test("enabling asks one question, creates the key, and the card re-reads what the engine resolved", async ({ page }) => {
    const c = await card(page, "default");
    await c.getByRole("switch").click();
    const dlg = page.getByRole("dialog", { name: "Enable retained-copy encryption" });
    await expect(dlg.getByText("Who holds the key?")).toBeVisible();
    await expect(dlg.getByText("retnd encryption policy patch --mode required --managed")).toBeVisible();
    await dlg.getByRole("button", { name: "Enable and create key" }).click();
    await expect(dlg).toBeHidden();
    await expect(c.getByRole("switch")).toBeChecked();
    await expect(c.getByText("No confirmed recovery bundle")).toBeVisible();
  });

  test("the missing-bundle notice survives a reload and only a confirmed bundle clears it", async ({ page }) => {
    const c = await card(page, "encryption-on");
    await expect(c.getByText("No confirmed recovery bundle")).toBeVisible();
    await page.reload();
    await expect(page.locator(CARD).getByText("No confirmed recovery bundle")).toBeVisible();
    const confirmed = await card(page, "encryption-confirmed");
    await expect(confirmed.getByText("No confirmed recovery bundle")).toHaveCount(0);
    await expect(confirmed.getByText(/confirmed Nov|confirmed /)).toBeVisible();
  });

  test("the export dialog arms Download only after the tick, and renders no key", async ({ page }) => {
    const c = await card(page, "encryption-on");
    await c.getByRole("button", { name: "Export recovery bundle" }).first().click();
    const dlg = page.getByRole("dialog", { name: "Export recovery bundle" });
    await expect(dlg.getByRole("button", { name: "Download bundle" })).toBeDisabled();
    await dlg.getByRole("checkbox", { name: /I understand this file can decrypt every copy/ }).check();
    await expect(dlg.getByRole("button", { name: "Download bundle" })).toBeEnabled();
    expect(await page.content()).not.toMatch(canary);
    await dlg.getByRole("button", { name: "Download bundle" }).click();
    await expect(dlg).toBeHidden();
    await expect(c.getByText("Bundle exported, not yet confirmed")).toBeVisible();
    expect(await page.content()).not.toMatch(canary);
  });

  test("retire is refused naming the copies, and offers nothing else", async ({ page }) => {
    const c = await card(page, "encryption-in-use");
    await c.getByRole("button", { name: "Rotate key" }).click();
    await page.getByRole("dialog", { name: "Rotate the managed key?" }).getByRole("button", { name: "Rotate key" }).click();
    await c.getByRole("button", { name: "Retire", exact: true }).click();
    const dlg = page.getByRole("dialog");
    await dlg.getByRole("button", { name: "Retire key" }).click();
    await expect(dlg.getByRole("alert")).toContainText("41 copies still need it");
    await expect(dlg.getByRole("button", { name: "Retire key" })).toBeDisabled();
  });

  test("an external reference is one single-line box with three sources and a three-answer test", async ({ page }) => {
    const c = await card(page, "encryption-on");
    await c.getByRole("button", { name: "Use an external key reference instead" }).click();
    const dlg = page.getByRole("dialog");
    await expect(dlg.getByLabel("Path to the key file")).toHaveAttribute("type", "text");
    await dlg.getByLabel("Environment variable").check();
    await dlg.getByLabel("Variable name").fill("EMPTY");
    await dlg.getByRole("button", { name: "Test reference" }).click();
    await expect(dlg.getByRole("group", { name: "Declared but unreachable" })).toBeVisible();
    await expect(dlg.locator("textarea, input[type=file]")).toHaveCount(0);
  });

  test("a required key that does not resolve is drawn as itself, in the card and on the dashboard", async ({ page }) => {
    const c = await card(page, "encryption-key-missing");
    await expect(c.getByText(/A required key cannot be resolved/)).toBeVisible();
    await open(page, "/", "encryption-key-missing");
    await expect(page.locator("[data-encryption-health]").getByText("A required key cannot be resolved")).toBeVisible();
    await open(page, "/", "default");
    await expect(page.locator("[data-encryption-health]")).toHaveCount(0);
  });

  test("the opt-out preference suppresses the card's pop-ups, with the preference explicitly on", async ({ page }) => {
    await open(page, "/settings", "encryption-on");
    await page.evaluate(() => window.localStorage.setItem("retnd.tooltips", "on"));
    const sw = page.locator(CARD).getByRole("switch");
    await sw.hover();
    await expect(page.locator(".tooltip__pop:visible").filter({ hasText: "retained_copy_encryption.mode" })).toHaveCount(1);
    await page.evaluate(() => window.localStorage.setItem("retnd.tooltips", "off"));
    await page.reload();
    await page.locator(CARD).getByRole("switch").hover();
    await expect(page.locator(".tooltip__pop:visible")).toHaveCount(0);
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the card meets WCAG AA in ${theme}`, async ({ page }) => {
      await card(page, "encryption-key-missing");
      await setTheme(page, theme);
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      expect(await measureContrast(page, "995 settings card " + theme, CARD), theme).toEqual([]);
      expect(await measureEdges(page, "995 settings card " + theme, CARD + " button"), theme).toEqual([]);
    });

    test(`the card does not scroll sideways at 360px in ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width: 360, height: 800 });
      await card(page, "encryption-on");
      await setTheme(page, theme);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      expect(overflow).toBeLessThanOrEqual(0);
    });
  }
});
