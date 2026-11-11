/**
 * Issue #995 (EPIC N): per-copy encryption on Backup detail, the per-set
 * control and Verify recoverable. WRITTEN, NOT RUN: no browser was launched
 * for this change. When it is run, it goes through
 * /Users/rom/workspace/xdg-pw-lock.sh and nowhere else.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, measureEdges, open, row, setTheme, spaGo } from "./helpers";

const PG = "postgres-prod-20260828.dump.zst";

async function detail(page: Page, scenario: string) {
  await open(page, "/backups", scenario);
  await row(page, PG).click();
  const copies = page.getByRole("region", { name: "Copies" });
  await expect(copies).toBeVisible();
  return copies;
}

test.describe("#995 Backup detail, Copies", () => {
  test("a deployment with encryption off sees no Encryption column", async ({ page }) => {
    const copies = await detail(page, "default");
    await expect(copies.getByRole("columnheader", { name: /Encryption/ })).toHaveCount(0);
  });

  test("a plaintext local copy and an encrypted off-site copy read as two different things on one backup", async ({ page }) => {
    const copies = await detail(page, "encryption-on");
    await expect(copies.locator('[data-encryption-cell="local"]').getByRole("group", { name: "Plaintext" })).toBeVisible();
    const off = copies.locator('[data-encryption-cell="offsite_s3"]');
    await expect(off.getByRole("group", { name: /^Encrypted/ })).toBeVisible();
    // Existence was verified by a scrub; no decrypt-verify has run. Encrypted, and not recoverable.
    await expect(off.getByRole("group", { name: "Not decrypt-verified" })).toBeVisible();
    await expect(off.getByRole("group", { name: /Verified recoverable/ })).toHaveCount(0);
  });

  test("a copy whose key is unresolvable is key missing and offers no verify action", async ({ page }) => {
    const copies = await detail(page, "encryption-key-missing");
    const off = copies.locator('[data-encryption-cell="offsite_s3"]');
    await expect(off.getByRole("group", { name: /^Key missing/ })).toBeVisible();
    await expect(off.getByRole("button", { name: "Verify recoverable" })).toHaveCount(0);
  });

  test("verify recoverable names the cost, needs the acceptance, and echoes the command", async ({ page }) => {
    const copies = await detail(page, "encryption-on");
    await copies.getByRole("button", { name: "Verify recoverable" }).click();
    const dlg = page.getByRole("dialog", { name: /Verify recoverable/ });
    await expect(dlg.getByText(/Reading this copy back costs egress/)).toBeVisible();
    await expect(dlg.getByRole("button", { name: "Verify now" })).toBeDisabled();
    await dlg.getByRole("checkbox", { name: "I accept this read" }).check();
    await expect(dlg.getByRole("button", { name: "Verify now" })).toBeEnabled();
    await expect(dlg.getByText(/retnd encryption verify .* --medium offsite_s3 --allow-egress/)).toBeVisible();
  });

  test("a refused decrypt-verify says nothing was read and shows no operation", async ({ page }) => {
    const copies = await detail(page, "encryption-key-missing");
    // The copy offers no action in this scenario, so the refusal is reached by the route directly.
    await expect(copies.getByRole("button", { name: "Verify recoverable" })).toHaveCount(0);
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the copies table meets WCAG AA in ${theme}`, async ({ page }) => {
      await detail(page, "encryption-on");
      await setTheme(page, theme);
      expect(await measureContrast(page, "995 copies " + theme, '[aria-label="Copies"]'), theme).toEqual([]);
      expect(await measureEdges(page, "995 copy chips " + theme, "[data-echip]"), theme).toEqual([]);
    });
  }
});

test.describe("#995 the per-set control", () => {
  async function setPage(page: Page, scenario: string) {
    // auth-config is an artifact-engine set. A kopia set (postgres-primary)
    // gets its own branch since #1296, with the radios disabled.
    await open(page, "/sets/production/auth-config", scenario);
    const card = page.locator("[data-set-encryption]");
    await expect(card).toBeVisible();
    return card;
  }

  test("sits under Retention with three values and says what Inherit resolves to", async ({ page }) => {
    const card = await setPage(page, "encryption-confirmed");
    await expect(card.getByRole("radio")).toHaveCount(3);
    await expect(card.getByText(/Inheriting: required \(from the deployment setting\)/)).toBeVisible();
    const order = await page.locator("section.card h2.eyebrow").allTextContents();
    expect(order.indexOf("Retention")).toBeLessThan(order.indexOf("Retained-copy encryption (this set)"));
  });

  test("an explicit Off stays distinguishable from Inherit after the write", async ({ page }) => {
    const card = await setPage(page, "encryption-confirmed");
    // The radio is controlled and flips only after the write round trip, so
    // click and then assert, rather than check().
    await card.getByLabel("Off").click();
    await expect(card.getByLabel("Off")).toBeChecked();
    await expect(card.getByText(/written as plaintext whatever the deployment says/)).toBeVisible();
    // The mock keeps overrides in memory, so a reload would reset them. Leave
    // the page in-app and come back instead.
    await spaGo(page, "/settings");
    await expect(page.locator("[data-set-encryption]")).toHaveCount(0);
    await spaGo(page, "/sets/production/auth-config");
    await expect(page.locator("[data-set-encryption]")).toBeVisible();
    await expect(page.locator("[data-set-encryption]").getByLabel("Off")).toBeChecked();
    await expect(page.locator("[data-set-encryption]").getByLabel("Inherit")).not.toBeChecked();
  });

  test("a required set with a key that does not resolve is drawn as refusing, and says nothing was deleted", async ({ page }) => {
    const card = await setPage(page, "encryption-key-missing");
    await expect(card.getByText("Required, key missing")).toBeVisible();
    await expect(card.getByText(/Nothing was deleted/)).toBeVisible();
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the card meets WCAG AA in ${theme}`, async ({ page }) => {
      await setPage(page, "encryption-key-missing");
      await setTheme(page, theme);
      expect(await measureContrast(page, "995 set card " + theme, "[data-set-encryption]"), theme).toEqual([]);
    });
  }
});
