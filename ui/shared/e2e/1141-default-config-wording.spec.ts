import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { measureContrast, open, setTheme, spaGo } from "./helpers";

/**
 * Issue #1141: a set that runs on the built-in default configuration, and has
 * never had a recovery test, must say both things wherever it talks about
 * Recovery, and never "Not configured".
 *
 * assurance-default-untested: the PostgreSQL set follows the deployment's
 * default (recovery tests off), and no recovery test has run on either of its
 * backups. Driven with wire=1 so the code a built app runs composes the claims.
 */
const S = "assurance-default-untested";
const SET = "/sets/production/postgres-primary";
const NEWEST = "postgres-prod-20260829.dump.zst";
const OLDER = "postgres-prod-20260828.dump.zst";
const NEWEST_PATH = "/backups/production/postgres-primary/" + NEWEST;
const NOTE = "No recovery test has run yet";
const DEFAULTS = "/settings#assurance-defaults";

const recovery = (scope: Locator) => scope.locator('[data-claim="recovery"]');
const listRow = (page: Page, file: string) => page.getByRole("row").filter({ has: page.locator("td.mono", { hasText: file }) });
const assuranceCard = (page: Page) => page.getByRole("region", { name: "Assurance" });
const setCard = (page: Page) => page.locator("article").filter({ hasText: "production/postgres-primary" });
const dash = (page: Page) => page.getByRole("region", { name: "Assurance summary" });

async function expectDefaultAndUntested(chip: Locator) {
  await expect(chip).toContainText("Default configuration");
  await expect(chip).not.toContainText("Not configured");
  await expect(chip.getByRole("link", { name: "Default configuration" })).toHaveAttribute("href", DEFAULTS);
  await expect(chip.getByTestId("recovery-untested")).toHaveText(NOTE);
}

test.describe("#1141 Recovery on a set that uses the default and has never been tested", () => {
  test("#1141 Backups list: both backups say Default configuration, link to Settings, and that no test has run", async ({ page }) => {
    await open(page, "/backups", S, { wire: true });
    for (const file of [NEWEST, OLDER]) await expectDefaultAndUntested(recovery(listRow(page, file)));
  });

  test("#1141 the link lands on the Settings Assurance card", async ({ page }) => {
    await open(page, "/backups", S, { wire: true });
    await recovery(listRow(page, NEWEST)).getByRole("link", { name: "Default configuration" }).click();
    await expect(page).toHaveURL(/\/settings#assurance-defaults$/);
    await expect(page.locator("#assurance-defaults")).toBeInViewport();
  });

  test("#1141 backup detail: the Assurance card, the lifecycle and Copies agree with the list", async ({ page }) => {
    await open(page, NEWEST_PATH, S, { wire: true });
    await expectDefaultAndUntested(recovery(assuranceCard(page)));
    const lifecycle = page.getByRole("listitem").filter({ hasText: "STRUCTURE READABLE" });
    await expect(lifecycle).toContainText("No recovery test has run on this backup");
    await expect(lifecycle).not.toContainText("Not configured");
    const copies = page.getByRole("region", { name: "Copies" });
    await expect(copies.getByTestId("copy-structure")).toHaveText("No recovery test has run on this copy");
    await expect(copies).not.toContainText("Not configured");
  });

  test("#1141 the set's page says the same", async ({ page }) => {
    await open(page, SET, S, { wire: true });
    await expectDefaultAndUntested(recovery(assuranceCard(page)));
  });

  test("#1141 the Backup Sets card says the same", async ({ page }) => {
    await open(page, "/sets", S, { wire: true });
    const field = setCard(page).getByTestId("field-recovery");
    await expect(field).toContainText("Default configuration");
    await expect(field).not.toContainText("Not configured");
    await expect(field.getByRole("link", { name: "Default configuration" })).toHaveAttribute("href", DEFAULTS);
    await expect(field.getByTestId("recovery-untested")).toHaveText(NOTE);
  });

  test("#1141 the Dashboard keeps its zeros but does not read as all clear", async ({ page }) => {
    await open(page, "/", S, { wire: true });
    const tile = dash(page);
    await expect(tile.getByTestId("assurance-due")).toContainText("0");
    await expect(tile.getByTestId("assurance-overdue")).toContainText("0");
    await expect(tile.getByTestId("assurance-degraded")).toContainText("0");
    await expect(tile.getByTestId("assurance-untested")).toContainText("2");
    const note = tile.getByTestId("assurance-untested-note");
    await expect(note).toContainText("No recovery test is on record for 2 of 2 backups, so a zero in Due does not mean they were tested.");
    await expect(note).toContainText("2 of them are in sets on the default configuration.");
    await expect(note.getByRole("link", { name: "default configuration" })).toHaveAttribute("href", DEFAULTS);
    await expect(tile).not.toContainText("Not configured");
  });
});

test.describe("#1141 while the set's policy has not arrived", () => {
  test("#1141 control: a policy that is still loading says nothing about the configuration, then says all of it", async ({ page }) => {
    await page.addInitScript(() => {
      let release!: () => void;
      const gate = new Promise<void>((r) => { release = r; });
      (window as unknown as Record<string, unknown>).__retndPolicy = { gate };
      (window as unknown as Record<string, unknown>).__retndPolicyRelease = release;
    });
    await open(page, "/backups", S, { wire: true });
    const chip = recovery(listRow(page, NEWEST));
    await expect(chip).toContainText("Unknown");
    await expect(chip).not.toContainText("Not configured");
    await expect(chip).not.toContainText("Default configuration");
    await page.evaluate(() => (window as unknown as { __retndPolicyRelease: () => void }).__retndPolicyRelease());
    await expectDefaultAndUntested(chip);
  });

  test("#1141 a policy that could not be read says so, and does not claim the set is unconfigured or untested", async ({ page }) => {
    await page.addInitScript(() => {
      (window as unknown as Record<string, unknown>).__retndPolicy = { fail: true };
    });
    await open(page, "/backups", S, { wire: true });
    const chip = recovery(listRow(page, NEWEST));
    await expect(chip).toContainText("Configuration unavailable");
    await expect(chip).not.toContainText("Not configured");
    await expect(chip).not.toContainText("Default configuration");
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`#1141 the new words meet AA in the ${theme} theme`, async ({ page }) => {
    await open(page, "/backups", S, { wire: true });
    await setTheme(page, theme);
    await expect(recovery(listRow(page, NEWEST)).getByTestId("recovery-untested")).toBeVisible();
    expect(await measureContrast(page, `list recovery chip, ${theme}`, '[data-claim="recovery"]')).toEqual([]);
    expect(await measureContrast(page, `NEW list note "No recovery test has run yet", ${theme}`, '[data-testid="recovery-untested"]')).toEqual([]);
    expect(await measureContrast(page, `NEW list link "Default configuration", ${theme}`, '[data-claim="recovery"] a.alink')).toEqual([]);
    await spaGo(page, "/sets");
    await setTheme(page, theme);
    await expect(setCard(page).getByTestId("recovery-untested")).toBeVisible();
    expect(await measureContrast(page, `set card fields, ${theme}`, "article dl")).toEqual([]);
    await spaGo(page, "/");
    await setTheme(page, theme);
    await expect(dash(page).getByTestId("assurance-untested-note")).toBeVisible();
    expect(await measureContrast(page, `dashboard tally, ${theme}`, '[aria-label="Assurance summary"] .card__body')).toEqual([]);
    expect(await measureContrast(page, `NEW dashboard sentence and link "default configuration", ${theme}`, '[data-testid="assurance-untested-note"]')).toEqual([]);
  });
}
