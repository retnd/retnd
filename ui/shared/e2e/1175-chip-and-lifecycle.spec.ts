import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { measureContrast, open, setTheme, spaGo } from "./helpers";

/**
 * Issue #1175: the verified chip and the Lifecycle list agree about a backup,
 * and the Recovery chip reads Default configuration or Custom configuration
 * (never Not configured) and links to where recovery is configured.
 *
 * assurance-default-untested: the PostgreSQL set follows the deployment's
 * default, both backups carry no ingest-time validation, and both were
 * content verified by a scrub (10h and 26h ago). Driven with wire=1 so the
 * code a built app runs composes the claims.
 */
const S = "assurance-default-untested";
const SET = "/sets/production/postgres-primary";
const NEWEST = "postgres-prod-20260829.dump.zst";
const NEWEST_PATH = "/backups/production/postgres-primary/" + NEWEST;
const DEFAULTS = "/settings#assurance";
const OWN = SET + "#assurance";

const recovery = (scope: Locator) => scope.locator('[data-claim="recovery"]');
const content = (scope: Locator) => scope.locator('[data-claim="content"]');
const listRow = (page: Page, file: string) => page.getByRole("row").filter({ has: page.locator("td.mono", { hasText: file }) });
const assuranceCard = (page: Page) => page.getByRole("region", { name: "Assurance" });
const step = (page: Page, label: string) => page.getByRole("listitem").filter({ hasText: new RegExp("^" + label) });

/** Gives the PostgreSQL set a policy of its own, the way an operator does. */
async function giveOwnPolicy(page: Page) {
  await open(page, SET, S, { wire: true });
  const section = assuranceCard(page);
  await section.getByRole("radio", { name: "This set's own policy" }).check();
  await section.getByRole("button", { name: "Save policy" }).click();
  await expect(section.getByText("Saved")).toBeVisible();
}

test.describe("#1175 the verified chip and the Lifecycle list", () => {
  test("#1175 a backup whose Content chip says verified has its VERIFIED step reached, with a time", async ({ page }) => {
    await open(page, NEWEST_PATH, S, { wire: true });
    await expect(content(assuranceCard(page))).toContainText("Content verified");
    await expect(step(page, "VERIFIED")).toContainText(/\d\d:\d\d:\d\d/);
    await expect(step(page, "VERIFIED")).not.toContainText("\u2014");
  });

  test("#1175 the VERIFIED step says what verified it, because nothing at ingest did", async ({ page }) => {
    await open(page, NEWEST_PATH, S, { wire: true });
    // A scrub compares against the digest taken at receipt, so with no ingest
    // verdict what it proves is that the content is intact since then.
    await expect(step(page, "VERIFIED")).toContainText("A scrub found the content intact since receipt");
    await expect(step(page, "VERIFIED")).not.toContainText("producer manifest");
  });

  test("#1175 a scrub does not reach VERIFIED when the ingest check failed", async ({ page }) => {
    // The newest backup failed its producer-manifest check at ingest, and a
    // scrub has since read it back and matched the digest taken at receipt.
    // That only proves the bad file is still the same bad file.
    await open(page, NEWEST_PATH, "assurance-ingest-failed-scrubbed", { wire: true });
    await expect(step(page, "CONTENT VERIFIED")).toContainText(/\d\d:\d\d:\d\d/);
    await expect(step(page, "VERIFIED")).toContainText("\u2014");
    await expect(step(page, "VERIFIED")).not.toContainText(/\d\d:\d\d:\d\d/);
    await expect(step(page, "VERIFIED")).toContainText("The ingest check failed");
    await expect(step(page, "VERIFIED")).not.toContainText("intact since receipt");
  });

  test("#1175 the list, the chip and the Lifecycle all say verified for the same backup", async ({ page }) => {
    await open(page, "/backups", S, { wire: true });
    await expect(content(listRow(page, NEWEST))).toContainText("Content verified");
    await spaGo(page, NEWEST_PATH);
    await expect(step(page, "CONTENT VERIFIED")).toContainText(/\d\d:\d\d:\d\d/);
    await expect(step(page, "VERIFIED")).toContainText(/\d\d:\d\d:\d\d/);
  });
});

test.describe("#1175 Recovery reads Default configuration or Custom configuration", () => {
  test("#1175 on the default, the chip says Default configuration and links to the Settings assurance card", async ({ page }) => {
    await open(page, "/backups", S, { wire: true });
    const chip = recovery(listRow(page, NEWEST));
    await expect(chip).toContainText("Default configuration");
    await expect(chip).not.toContainText("Not configured");
    await expect(chip.getByRole("link", { name: "Default configuration" })).toHaveAttribute("href", DEFAULTS);
  });

  test("#1175 once the set has a policy of its own, the Backups chip says Custom configuration and links to that set's policy", async ({ page }) => {
    await giveOwnPolicy(page);
    await spaGo(page, "/backups");
    const chip = recovery(listRow(page, NEWEST));
    await expect(chip).toContainText("Custom configuration");
    await expect(chip).not.toContainText("Not configured");
    await expect(chip).not.toContainText("own policy");
    await expect(chip.getByRole("link", { name: "Custom configuration" })).toHaveAttribute("href", OWN);
  });

  test("#1175 the Custom configuration link lands on the set's recovery policy", async ({ page }) => {
    await giveOwnPolicy(page);
    await spaGo(page, "/backups");
    await recovery(listRow(page, NEWEST)).getByRole("link", { name: "Custom configuration" }).click();
    await expect(page).toHaveURL(/\/sets\/production\/postgres-primary#assurance$/);
    await expect(page.locator("#assurance")).toBeInViewport();
  });

  test("#1175 the backup page, the set page and the set card say Custom configuration too", async ({ page }) => {
    await giveOwnPolicy(page);
    await spaGo(page, NEWEST_PATH);
    await expect(recovery(assuranceCard(page))).toContainText("Custom configuration");
    await spaGo(page, "/sets");
    const field = page.locator("article").filter({ hasText: "production/postgres-primary" }).getByTestId("field-recovery");
    await expect(field).toContainText("Custom configuration");
    await expect(field.getByRole("link", { name: "Custom configuration" })).toHaveAttribute("href", OWN);
  });

  test("#1175 the policy form no longer tells an operator the Recovery claim reads Not configured", async ({ page }) => {
    await open(page, SET, S, { wire: true });
    const section = assuranceCard(page);
    await section.getByRole("radio", { name: "This set's own policy" }).check();
    await expect(section).toContainText("The recovery test is off for this set");
    await expect(section).not.toContainText("Not configured");
    await expect(section).toContainText("Custom configuration");
  });

  test("#1175 no surface of the set reads Not configured", async ({ page }) => {
    await open(page, "/", S, { wire: true });
    await expect(page.getByRole("region", { name: "Assurance summary" })).not.toContainText("Not configured");
    for (const path of ["/backups", NEWEST_PATH, SET, "/sets"]) {
      await spaGo(page, path);
      await expect(page.locator("main")).not.toContainText("Not configured");
    }
  });
});

test.describe("#1175 contrast of the Recovery chip link", () => {
  for (const theme of ["light", "dark"] as const) {
    test(`#1175 the Custom configuration chip and link meet AA in the ${theme} theme`, async ({ page }) => {
      await giveOwnPolicy(page);
      await spaGo(page, "/backups");
      await expect(recovery(listRow(page, NEWEST))).toContainText("Custom configuration");
      await setTheme(page, theme);
      expect(await measureContrast(page, `#1175 Recovery chip, ${theme}`, '[data-claim="recovery"]')).toEqual([]);
    });
  }
});
