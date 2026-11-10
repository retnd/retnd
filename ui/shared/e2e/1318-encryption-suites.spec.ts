/**
 * Issue #1318 (EPIC T, T2.3): encryption suites in the browser. The suite
 * choice on the Settings card, the rotate-then-confirm flow, the suite id on
 * every encrypted copy, the deprecated and retired health rows with the
 * re-encrypt dry run, and the 80-character clip over a planted post-quantum
 * recipient, in light and dark.
 *
 * WRITTEN, NOT RUN: no browser was launched for this change. When it is run,
 * it goes through /Users/rom/workspace/xdg-pw-lock.sh and nowhere else, one
 * run at a time.
 *
 * Driven against the mock's suite scenarios (encryption/mockEncryption.ts):
 * "encryption-suite-change" has an active age/v1 key and a policy pinned to
 * age/v1, so choosing age-pq/v1 answers rotation_needed;
 * "encryption-suite-lifecycle" has age/v1 deprecated (retiring in 0.9.0) and
 * mock-retired/v1 retired, both still recorded by copies;
 * "encryption-suite-canary" plants a 2000-character recipient-shaped string in
 * the places a hostile or careless server could put one.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, measureEdges, open, row, setTheme, spaGo } from "./helpers";

const CARD = "[data-encryption-card]";
// The planted value is the mock's own (PQ_CANARY in mockEncryption.ts): its
// head is distinctive and its tail is what must never reach the page.
const CANARY_TAIL = /CANARY1318TAIL/;

async function card(page: Page, scenario: string) {
  await open(page, "/settings", scenario);
  const c = page.locator(CARD);
  await expect(c).toBeVisible();
  return c;
}

test.describe("#1318 suite-select-lists-registry", () => {
  test("lists every suite with its status and sentence, and marks the default", async ({ page }) => {
    const c = await card(page, "encryption-suite-change");
    const group = c.getByRole("radiogroup", { name: "Suite" });
    await expect(group.getByRole("radio")).toHaveCount(3);
    const pq = group.locator('[data-suite-option="age-pq/v1"]');
    await expect(pq.getByText("Default", { exact: true })).toBeVisible();
    await expect(pq.locator("[data-suite-description]")).not.toBeEmpty();
    await expect(group.locator('[data-suite-option="age/v1"]').getByText("Supported", { exact: true })).toBeVisible();
    // The policy is pinned to age/v1 in this scenario, and the card says so.
    await expect(group.getByRole("radio", { name: /^age\/v1/ })).toBeChecked();
    await expect(c.getByText(/Configured: age\/v1/)).toBeVisible();
    await expect(c.getByText("retnd encryption suites", { exact: true })).toBeVisible();
  });

  test("a deprecated suite names the version it retires in", async ({ page }) => {
    const c = await card(page, "encryption-suite-lifecycle");
    await expect(c.locator('[data-suite-option="age/v1"]').getByText("Deprecated, retiring in 0.9.0")).toBeVisible();
  });

  test("off draws no suite group", async ({ page }) => {
    const c = await card(page, "default");
    await expect(c.getByRole("radiogroup", { name: "Suite" })).toHaveCount(0);
  });
});

test.describe("#1318 retired-option-disabled-with-reason", () => {
  test("a retired suite is shown, disabled, with the reason it cannot be chosen", async ({ page }) => {
    const c = await card(page, "encryption-suite-lifecycle");
    const retired = c.locator('[data-suite-option="mock-retired/v1"]');
    await expect(retired.getByRole("radio")).toBeDisabled();
    await expect(retired.getByText("Retired", { exact: true })).toBeVisible();
    await expect(retired.locator("[data-suite-reason]")).toContainText(/nothing new is written under it/);
  });
});

test.describe("#1318 suite-change-leads-through-rotate-bundle-confirm", () => {
  test("patch, rotate, download and confirm, in that order, with the degraded state between", async ({ page }) => {
    const c = await card(page, "encryption-suite-change");
    await c.getByRole("radio", { name: /^age-pq\/v1/ }).check();
    const dlg = page.getByRole("dialog", { name: "Change the suite" });
    await expect(dlg.getByText("retnd encryption policy patch --suite age-pq/v1")).toBeVisible();
    await expect(dlg.locator('[data-suite-step="patch"]')).toHaveAttribute("data-state", "now");
    await dlg.getByRole("button", { name: "Change suite" }).click();

    // rotation_needed: the same dialog moves to Rotate key.
    await expect(dlg.locator('[data-suite-step="rotate"]')).toHaveAttribute("data-state", "now");
    await expect(dlg.getByText("retnd encryption key rotate --suite age-pq/v1")).toBeVisible();
    // Between the steps the card already says what is missing.
    await expect(c.getByText("No active key for suite age-pq/v1")).toBeVisible();
    await dlg.getByRole("button", { name: "Rotate key" }).click();

    await expect(dlg.locator('[data-suite-step="bundle"]')).toHaveAttribute("data-state", "now");
    await expect(c.getByText("No active key for suite age-pq/v1")).toHaveCount(0);
    await expect(c.getByText("No confirmed recovery bundle")).toBeVisible();
    await expect(dlg.getByRole("button", { name: "Download bundle" })).toBeDisabled();
    await dlg.getByRole("checkbox", { name: /I understand this file can decrypt every copy/ }).check();
    await dlg.getByRole("button", { name: "Download bundle" }).click();

    await expect(dlg.locator('[data-suite-step="confirm"]')).toHaveAttribute("data-state", "now");
    await expect(c.getByText("Bundle exported, not yet confirmed")).toBeVisible();
    await dlg.getByRole("button", { name: "I have stored the bundle safely" }).click();

    await expect(dlg).toBeHidden();
    await expect(c.getByText("No confirmed recovery bundle")).toHaveCount(0);
    await expect(c.getByText("Bundle exported, not yet confirmed")).toHaveCount(0);
    await expect(c.getByRole("radio", { name: /^age-pq\/v1/ })).toBeChecked();
  });

  test("leaving early says what is still owed, keeps the policy patched, and the health state shows it", async ({ page }) => {
    const c = await card(page, "encryption-suite-change");
    await c.getByRole("radio", { name: /^age-pq\/v1/ }).check();
    const dlg = page.getByRole("dialog", { name: "Change the suite" });
    await dlg.getByRole("button", { name: "Change suite" }).click();
    await expect(dlg.locator('[data-suite-step="rotate"]')).toHaveAttribute("data-state", "now");

    await page.keyboard.press("Escape");
    // Not closed: the dialog lists what is owed first.
    await expect(page.getByRole("dialog", { name: "Still owed" })).toBeVisible();
    const owed = page.getByRole("dialog", { name: "Still owed" });
    await expect(owed.locator("[data-owed]")).toHaveCount(3);
    await expect(owed.getByText(/An active key for suite age-pq\/v1/)).toBeVisible();
    await owed.getByRole("button", { name: "Back to the steps" }).click();
    await expect(page.getByRole("dialog", { name: "Change the suite" })).toBeVisible();

    await page.keyboard.press("Escape");
    await page.getByRole("dialog", { name: "Still owed" }).getByRole("button", { name: "Leave, I will finish later" }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(c.getByRole("radio", { name: /^age-pq\/v1/ })).toBeChecked();
    await expect(c.getByText("No active key for suite age-pq/v1")).toBeVisible();

    // The Dashboard carries it too (same page, so the mock keeps the write).
    await spaGo(page, "/");
    await expect(page.locator("[data-encryption-health]").getByText("No active key for suite age-pq/v1")).toBeVisible();
  });

  test("cancelling before the patch writes nothing and needs no owed list", async ({ page }) => {
    const c = await card(page, "encryption-suite-change");
    await c.getByRole("radio", { name: /^age-pq\/v1/ }).check();
    const dlg = page.getByRole("dialog", { name: "Change the suite" });
    await dlg.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(c.getByRole("radio", { name: /^age\/v1/ })).toBeChecked();
  });
});

test.describe("#1318 per-copy-chip-shows-suite", () => {
  test("an age/v1 and an age-pq/v1 copy on one page are told apart", async ({ page }) => {
    await open(page, "/backups", "encryption-suite-lifecycle");
    const pg = row(page, "postgres-prod-20260828.dump.zst").locator('[data-list-copy="offsite_s3"]');
    const billing = row(page, "billing-20260827.sql.gz").locator('[data-list-copy="offsite_cold"]');
    await expect(pg.getByRole("group", { name: /^Encrypted age\/v1/ })).toBeVisible();
    await expect(billing.getByRole("group", { name: /^Encrypted age-pq\/v1/ })).toBeVisible();
  });

  test("Backup detail shows the suite on the chip and names it on the stored-bytes digest line", async ({ page }) => {
    await open(page, "/backups", "encryption-suite-lifecycle");
    await row(page, "postgres-prod-20260828.dump.zst").click();
    const copies = page.getByRole("region", { name: "Copies" });
    const off = copies.locator('[data-encryption-cell="offsite_s3"]');
    await expect(off.getByRole("group", { name: /^Encrypted age\/v1/ })).toBeVisible();
    // The second chip is unchanged.
    await expect(off.getByRole("group", { name: "Not decrypt-verified" })).toBeVisible();
    await expect(page.getByText(/\(suite age\/v1 v1, key /)).toBeVisible();
  });
});

test.describe("#1318 health-tile-shows-deprecated-counts", () => {
  test("deprecated and retired suites still recorded are counted, in the health sentence", async ({ page }) => {
    await open(page, "/", "encryption-suite-lifecycle");
    const h = page.locator("[data-encryption-health]");
    await expect(h.locator('[data-suite-health="age/v1"]')).toContainText(
      "12 active copies are under a deprecated suite (age/v1); retirement is announced for version 0.9.0."
    );
    await expect(h.locator('[data-suite-health="mock-retired/v1"]')).toContainText(
      "3 active copies are under a retired suite (mock-retired/v1); it reads and never writes."
    );
  });

  test("Re-encrypt under current suite runs the dry run first and arms the run only after it", async ({ page }) => {
    await open(page, "/", "encryption-suite-lifecycle");
    await page.locator('[data-suite-health="age/v1"]').getByRole("button", { name: "Re-encrypt under current suite" }).click();
    const dlg = page.getByRole("dialog", { name: "Re-encrypt under current suite" });
    await expect(dlg.getByText("retnd encryption reencrypt --suite age/v1 --dry-run")).toBeVisible();
    await expect(dlg.locator("[data-reencrypt-plan]")).toBeVisible();
    await expect(dlg.getByText(/same medium only/)).toBeVisible();
    const run = dlg.getByRole("button", { name: /^Re-encrypt \d+ copies$/ });
    await expect(run).toBeEnabled();
    await expect(dlg.getByText("retnd encryption reencrypt --suite age/v1", { exact: true })).toBeVisible();
    await run.click();
    await expect(dlg.getByText(/Started/)).toBeVisible();
  });

  test("a deployment with every copy under supported suites draws no suite rows", async ({ page }) => {
    await open(page, "/", "encryption-confirmed");
    await expect(page.locator("[data-suite-health]")).toHaveCount(0);
  });
});

test.describe("#1318 no recipient-length string is drawn whole", () => {
  test("the Settings card and the backup page clip a planted 2000-character recipient", async ({ page }) => {
    const c = await card(page, "encryption-suite-canary");
    await expect(c.getByRole("radiogroup", { name: "Suite" })).toBeVisible();
    expect(await page.content()).not.toMatch(CANARY_TAIL);
    await expect(c.getByRole("button", { name: "Copy" }).first()).toBeVisible();

    await open(page, "/backups", "encryption-suite-canary");
    await row(page, "postgres-prod-20260828.dump.zst").click();
    await expect(page.getByRole("region", { name: "Copies" })).toBeVisible();
    expect(await page.content()).not.toMatch(CANARY_TAIL);
  });
});

for (const theme of ["light", "dark"] as const) {
  test.describe(`#1318 contrast in ${theme}`, () => {
    test(`the suite group, its badges and the retired option meet WCAG AA in ${theme}`, async ({ page }) => {
      await card(page, "encryption-suite-lifecycle");
      await setTheme(page, theme);
      expect(await measureContrast(page, "1318 suite group " + theme, '[role="radiogroup"][aria-label="Suite"]'), theme).toEqual([]);
      expect(await measureEdges(page, "1318 suite badges " + theme, "[data-suite-badge]"), theme).toEqual([]);
    });

    test(`the change dialog and its step strip meet WCAG AA in ${theme}`, async ({ page }) => {
      const c = await card(page, "encryption-suite-change");
      await setTheme(page, theme);
      await c.getByRole("radio", { name: /^age-pq\/v1/ }).check();
      expect(await measureContrast(page, "1318 change dialog " + theme, '[role="dialog"]'), theme).toEqual([]);
      expect(await measureEdges(page, "1318 steps " + theme, "[data-suite-step]"), theme).toEqual([]);
    });

    test(`the chip with its suite id meets WCAG AA in ${theme}`, async ({ page }) => {
      await open(page, "/backups", "encryption-suite-lifecycle");
      await setTheme(page, theme);
      expect(await measureContrast(page, "1318 suite chip " + theme, "[data-echip][data-state=encrypted]"), theme).toEqual([]);
    });

    test(`the health rows and the dry-run dialog meet WCAG AA in ${theme}`, async ({ page }) => {
      await open(page, "/", "encryption-suite-lifecycle");
      await setTheme(page, theme);
      expect(await measureContrast(page, "1318 health " + theme, "[data-encryption-health]"), theme).toEqual([]);
      await page.locator('[data-suite-health="age/v1"]').getByRole("button", { name: "Re-encrypt under current suite" }).click();
      await expect(page.locator("[data-reencrypt-plan]")).toBeVisible();
      expect(await measureContrast(page, "1318 reencrypt " + theme, '[role="dialog"]'), theme).toEqual([]);
    });
  });
}
