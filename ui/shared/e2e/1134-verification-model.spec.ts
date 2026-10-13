import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { measureContrast, open, setTheme, spaGo } from "./helpers";

/**
 * Issue #1134: one backup has one set of verification facts, and every surface
 * reads them from one model. Driven with `wire=1` so realAssurance (the code a
 * built app runs) composes the claims from the mock's placements.
 *
 * assurance-default-policy: the PostgreSQL set follows the deployment's
 * default (recovery tests off, no validator, so a manual run is the built-in
 * structure check). Its newest backup (20260829) was content verified 10h ago
 * and has no recovery record. The backup before it (20260828) carries the
 * set's only recovery test, readable by the built-in check 5h ago. Neither has
 * an ingest-time validation, and the Backup Sets cards carry what the real
 * client leaves unreported.
 */
const S = "assurance-default-policy";
const SET = "/sets/production/postgres-primary";
const NEWEST = "postgres-prod-20260829.dump.zst";
const TESTED = "postgres-prod-20260828.dump.zst";
const NEWEST_PATH = "/backups/production/postgres-primary/" + NEWEST;
const TESTED_PATH = "/backups/production/postgres-primary/" + TESTED;

const recovery = (scope: Locator) => scope.locator('[data-claim="recovery"]');
// By the file's own cell: the newest backup's Recovery note names the older file too.
const listRow = (page: Page, file: string) => page.getByRole("row").filter({ has: page.locator("td.mono", { hasText: file }) });
const assuranceCard = (page: Page) => page.getByRole("region", { name: "Assurance" });
const step = (page: Page, label: string) => page.getByRole("listitem").filter({ hasText: label });
const setCard = (page: Page) => page.locator("article").filter({ hasText: "production/postgres-primary" });
const dash = (page: Page) => page.getByRole("region", { name: "Assurance summary" });

async function chipText(scope: Locator) {
  return ((await recovery(scope).innerText()) ?? "").replace(/\s+/g, " ").trim();
}

test.describe("#1134 Recovery on the Backups list", () => {
  test("#1134 a backup in a set that follows the default says Default configuration, with the effective state beside it", async ({ page }) => {
    await open(page, "/backups", S, { wire: true });
    const chip = recovery(listRow(page, NEWEST));
    await expect(chip).toContainText("Default configuration");
    await expect(chip).not.toContainText("Not configured");
    await expect(chip.getByRole("link", { name: "Default configuration" })).toHaveAttribute("href", "/settings#assurance-defaults");
    await expect(chip.getByTestId("recovery-effective")).toHaveText("Recovery tests are off, a manual run uses the built-in check");
    await expect(chip).toHaveAttribute("data-state", "never_configured");
  });

  test("#1134 following the link lands on the Settings Assurance card, scrolled to it", async ({ page }) => {
    await open(page, "/backups", S, { wire: true });
    await recovery(listRow(page, NEWEST)).getByRole("link", { name: "Default configuration" }).click();
    await expect(page).toHaveURL(/\/settings#assurance-defaults$/);
    const card = page.locator("#assurance-defaults");
    await expect(card).toBeVisible();
    await expect(card).toBeInViewport();
    await expect(card.getByRole("heading", { name: "Assurance" })).toBeVisible();
  });

  test("#1134 the set's newest test, which is on the other backup, is said beside the backup's own state and links to it", async ({ page }) => {
    await open(page, "/backups", S, { wire: true });
    const note = recovery(listRow(page, NEWEST)).getByTestId("recovery-elsewhere");
    await expect(note).toContainText("Newest recovery test of this set: readable (built-in check) on " + TESTED);
    await expect(note).toContainText("5h ago");
    await expect(note.getByRole("link", { name: TESTED })).toHaveAttribute("href", TESTED_PATH);
    // the older backup carries its own result and does not point at itself
    const own = recovery(listRow(page, TESTED));
    await expect(own).toContainText("Recovered, readable");
    await expect(own).toHaveAttribute("data-state", "pass_builtin");
    await expect(own.getByTestId("recovery-elsewhere")).toHaveCount(0);
    await expect(own.getByTestId("recovery-effective")).toHaveCount(0);
  });

  test("#1134 the list can be narrowed to the backups with no recovery test, and says how many it left", async ({ page }) => {
    await open(page, "/backups?assurance=untested", S, { wire: true });
    await expect(page.getByTestId("assurance-filter-status")).toHaveText("Showing 1 of 2 backups with no recovery test");
    await expect(listRow(page, NEWEST)).toHaveCount(1);
    await expect(listRow(page, TESTED)).toHaveCount(0);
    await page.getByRole("button", { name: "Show all backups" }).click();
    await expect(listRow(page, TESTED)).toHaveCount(1);
    await expect(page.getByTestId("assurance-filter-status")).toHaveCount(0);
  });
});

test.describe("#1134 the backup's own page", () => {
  test("#1134 the Assurance card says what the Backups list says", async ({ page }) => {
    await open(page, "/backups", S, { wire: true });
    await expect(recovery(listRow(page, NEWEST))).toContainText("Default configuration");
    const onList = await chipText(listRow(page, NEWEST));
    await spaGo(page, NEWEST_PATH);
    await expect(recovery(assuranceCard(page))).toContainText("Default configuration");
    expect(await chipText(assuranceCard(page))).toBe(onList);
    await expect(recovery(assuranceCard(page)).getByRole("link", { name: "Default configuration" })).toBeVisible();
  });

  test("#1134 the lifecycle shows content verified and structure readable from the recorded facts", async ({ page }) => {
    await open(page, TESTED_PATH, S, { wire: true });
    await expect(step(page, "CONTENT VERIFIED")).toContainText(/\d\d:\d\d:\d\d/);
    await expect(step(page, "CONTENT VERIFIED")).not.toContainText("\u2014");
    await expect(step(page, "STRUCTURE READABLE")).toContainText(/\d\d:\d\d:\d\d/);
    await expect(step(page, "STRUCTURE READABLE")).toContainText("built-in");
    await expect(step(page, "STRUCTURE READABLE")).not.toContainText("\u2014");
  });

  test("#1134 a backup nobody has tested shows content verified and an unreached structure step", async ({ page }) => {
    await open(page, NEWEST_PATH, S, { wire: true });
    await expect(step(page, "CONTENT VERIFIED")).toContainText(/\d\d:\d\d:\d\d/);
    await expect(step(page, "STRUCTURE READABLE")).toContainText("\u2014");
    await expect(step(page, "STRUCTURE READABLE")).toContainText("No recovery test has run on this backup");
  });

  test("#1134 Validation result no longer says nothing has validated a backup that was content verified", async ({ page }) => {
    await open(page, NEWEST_PATH, S, { wire: true });
    const row = page.getByTestId("validation-result");
    await expect(row).toContainText("Not recorded at ingest.");
    await expect(row).toContainText("Content verified");
    await expect(row).toContainText("the copy was read back and its hash matched");
    await expect(row).not.toContainText("nothing has validated");
  });

  test("#1134 Copies says what the copy's recovery record is, and that there is none when there is none", async ({ page }) => {
    await open(page, TESTED_PATH, S, { wire: true });
    const copies = page.getByRole("region", { name: "Copies" });
    await expect(copies).toContainText("Content verified");
    await expect(copies.getByTestId("copy-structure")).toContainText("Structure readable");
    await expect(copies.getByTestId("copy-structure")).toContainText("built-in check");
    await spaGo(page, NEWEST_PATH);
    await expect(copies.getByTestId("copy-structure")).toHaveText("No recovery test has run on this copy");
  });
});

test.describe("#1134 the set's page", () => {
  test("#1134 the Assurance card says what the Backups list says, with the link", async ({ page }) => {
    await open(page, SET, S, { wire: true });
    const chip = recovery(assuranceCard(page));
    await expect(chip).toContainText("Default configuration");
    await expect(chip).not.toContainText("Not configured");
    await expect(chip.getByRole("link", { name: "Default configuration" })).toHaveAttribute("href", "/settings#assurance-defaults");
    await expect(chip.getByTestId("recovery-elsewhere")).toContainText(TESTED);
  });

  test("#1134 the validator output says readable in words, and agrees with the chip", async ({ page }) => {
    await open(page, SET, S, { wire: true });
    await assuranceCard(page).getByRole("button", { name: "Show validator output" }).click();
    const facts = page.getByTestId("validator-facts");
    await expect(facts).toContainText("found the copy readable (the built-in structure check)");
    await expect(facts).not.toContainText("has the state readable");
    await expect(facts).toContainText(TESTED);
  });

  test("#1134 the Validation block says what ran, and does not call an unreported setting not enabled", async ({ page }) => {
    await open(page, SET, S, { wire: true });
    const block = page.getByTestId("validation-block");
    await expect(block.getByTestId("validation-ran-structure")).toContainText("Built-in structure check: readable");
    await expect(block.getByTestId("validation-ran-structure")).toContainText("on " + TESTED);
    await expect(block.getByTestId("validation-ran-structure")).toContainText("proves the file decodes end to end and matches its digest, not that an application can use it");
    await expect(block.getByTestId("validation-ran-content")).toContainText("Content verified");
    await expect(block.getByTestId("validation-ran-content")).toContainText(NEWEST);
    await expect(block).toContainText("Transfer verification");
    await expect(block).toContainText("Application validation");
    await expect(block).toContainText("not reported by this server");
    await expect(block).not.toContainText("not enabled");
  });

  test("#1134 a recovery test run from this page moves the chip and the Validation block together, and the list follows", async ({ page }) => {
    test.setTimeout(120_000);
    await open(page, SET, S, { wire: true });
    const block = page.getByTestId("validation-block");
    await expect(block.getByTestId("validation-ran-structure")).toContainText("on " + TESTED);
    await assuranceCard(page).getByRole("button", { name: "Run recovery test" }).click();
    const chip = recovery(assuranceCard(page));
    await expect(chip).toHaveAttribute("data-state", "pass_builtin", { timeout: 90_000 });
    await expect(block.getByTestId("validation-ran-structure")).toContainText("on " + NEWEST);
    await expect(chip.getByTestId("recovery-elsewhere")).toHaveCount(0);
    await spaGo(page, "/backups");
    await expect(recovery(listRow(page, NEWEST))).toContainText("Recovered, readable");
    await expect(recovery(listRow(page, NEWEST))).toHaveAttribute("data-state", "pass_builtin");
  });
});

test.describe("#1134 the Backup Sets cards", () => {
  test("#1134 Last validation, Last run, Retained and Expected every come from recorded facts or say why they are not known", async ({ page }) => {
    await open(page, "/sets", S, { wire: true });
    const card = setCard(page);
    await expect(card.getByTestId("field-last-validation")).toHaveText(/^Readable \(built-in check\) 5h ago$/);
    await expect(card.getByTestId("field-last-run")).toHaveText("Not recorded");
    await expect(card).not.toContainText(/last run\s*never/i);
    await expect(card.getByTestId("field-retained")).toHaveText("2 backups, size not reported");
    await expect(card.getByTestId("field-expected-every")).toHaveText("Not reported (stale after 36h)");
  });

  test("#1134 the card has a Recovery field that says what every other surface says", async ({ page }) => {
    await open(page, "/sets", S, { wire: true });
    const field = setCard(page).getByTestId("field-recovery");
    await expect(field).toContainText("Default configuration");
    await expect(field.getByRole("link", { name: "Default configuration" })).toHaveAttribute("href", "/settings#assurance-defaults");
    await expect(field).toContainText("Recovery tests are off, a manual run uses the built-in check");
  });
});

test.describe("#1134 the Dashboard tally", () => {
  test("#1134 zero Due does not read as tested: the tile counts the backups with no recovery test and says so", async ({ page }) => {
    await open(page, "/", S, { wire: true });
    const tile = dash(page);
    await expect(tile.getByTestId("assurance-due")).toContainText("0");
    await expect(tile.getByTestId("assurance-overdue")).toContainText("0");
    await expect(tile.getByTestId("assurance-degraded")).toContainText("0");
    await expect(tile.getByTestId("assurance-untested")).toContainText("1");
    await expect(tile.getByTestId("assurance-untested-note")).toHaveText(
      "No recovery test is on record for 1 of 2 backups, so a zero in Due does not mean they were tested."
    );
  });

  test("#1134 each count is a link to the list it counts, and a zero is disabled with its reason", async ({ page }) => {
    await open(page, "/", S, { wire: true });
    const tile = dash(page);
    const link = tile.getByRole("link", { name: "Show 1 backup with no recovery test" });
    await expect(link).toHaveAttribute("href", "/backups?assurance=untested");
    for (const name of ["Show 0 due backups", "Show 0 overdue backups", "Show 0 degraded backups"]) {
      const b = tile.getByRole("button", { name });
      await expect(b).toBeDisabled();
      await expect(b).toHaveAttribute("aria-describedby", /.+/);
    }
    await expect(tile.getByTestId("assurance-due-why")).toHaveText("Nothing is due.");
    await link.click();
    await expect(page).toHaveURL(/\/backups\?assurance=untested/);
    await expect(page.getByTestId("assurance-filter-status")).toHaveText("Showing 1 of 2 backups with no recovery test");
    await expect(listRow(page, NEWEST)).toHaveCount(1);
  });

  test("#1134 the tally and the list it links to are one definition: the counts follow a finished recovery test", async ({ page }) => {
    test.setTimeout(120_000);
    await open(page, SET, S, { wire: true });
    await assuranceCard(page).getByRole("button", { name: "Run recovery test" }).click();
    await expect(recovery(assuranceCard(page))).toHaveAttribute("data-state", "pass_builtin", { timeout: 90_000 });
    await spaGo(page, "/");
    await expect(dash(page).getByTestId("assurance-untested")).toContainText("0");
    await expect(dash(page).getByRole("button", { name: "Show 0 backups with no recovery test" })).toBeDisabled();
    await expect(dash(page).getByTestId("assurance-untested-note")).toHaveCount(0);
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`#1134 the new text on every surface meets AA in the ${theme} theme`, async ({ page }) => {
    await open(page, "/backups", S, { wire: true });
    await setTheme(page, theme);
    const chip = recovery(listRow(page, NEWEST));
    await expect(chip.getByTestId("recovery-effective")).toBeVisible();
    await expect(chip.getByTestId("recovery-elsewhere")).toBeVisible();
    expect(await measureContrast(page, `list recovery chip, ${theme}`, '[data-claim="recovery"]')).toEqual([]);

    await spaGo(page, NEWEST_PATH);
    await setTheme(page, theme);
    await expect(step(page, "STRUCTURE READABLE")).toBeVisible();
    expect(await measureContrast(page, `lifecycle, ${theme}`, "ol")).toEqual([]);
    await expect(page.getByTestId("copy-structure")).toBeVisible();
    expect(await measureContrast(page, `copy recovery line, ${theme}`, '[data-testid="copy-structure"]')).toEqual([]);
    await expect(page.getByTestId("validation-result")).toBeVisible();
    expect(await measureContrast(page, `validation result, ${theme}`, '[data-testid="validation-result"]')).toEqual([]);

    await spaGo(page, SET);
    await setTheme(page, theme);
    await expect(page.getByTestId("validation-ran-structure")).toBeVisible();
    expect(await measureContrast(page, `validation block, ${theme}`, '[data-testid="validation-block"]')).toEqual([]);

    await spaGo(page, "/sets");
    await setTheme(page, theme);
    await expect(setCard(page).getByTestId("field-recovery")).toBeVisible();
    expect(await measureContrast(page, `set card fields, ${theme}`, "article dl")).toEqual([]);

    await spaGo(page, "/");
    await setTheme(page, theme);
    await expect(dash(page).getByTestId("assurance-untested-note")).toBeVisible();
    await expect(dash(page).getByTestId("assurance-due-why")).toBeVisible();
    expect(await measureContrast(page, `dashboard tally, ${theme}`, '[aria-label="Assurance summary"] .card__body')).toEqual([]);
  });
}
