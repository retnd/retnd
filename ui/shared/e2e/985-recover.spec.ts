import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, row, measureBorders, measureContrast, setTheme } from "./helpers";

const PG = "postgres-prod-20260828.dump.zst";
const ARCHIVED = "billing-20260827.sql.gz";
const STORE = "retnd.mock.assurance";

async function toDetail(page: Page, file: string, scenario = "default") {
  await open(page, "/backups", scenario);
  await row(page, file).click();
}
const dialog = (page: Page) => page.getByRole("dialog", { name: "Recover artifact" });
async function openDialog(page: Page) {
  await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
  await expect(dialog(page)).toBeVisible();
}
const primary = (page: Page) => dialog(page).getByRole("button", { name: /^(Recover artifact|Ask for a new grant|Ask the provider to restore this copy|Accept the cost and recover)$/ });
const store = (page: Page) => page.evaluate((k) => JSON.parse(sessionStorage.getItem(k) ?? "{}"), STORE);

test.describe("#985 Recover artifact: choosing", () => {
  test("offers the three targets and echoes the command, over plain http", async ({ page }) => {
    await toDetail(page, PG);
    await openDialog(page);
    const dlg = dialog(page);
    for (const t of ["Download in this browser", "Under the staging root on this retnd host", "Configured destination"]) {
      await expect(dlg.getByRole("radio", { name: t })).toBeVisible();
    }
    // the browser form is the standard-output form, which is a target the CLI has
    await expect(dlg.getByText("retnd recover production/postgres-primary/" + PG + " --to - > recovered-file", { exact: true })).toBeVisible();
    // the page is served over http://127.0.0.1 and the dialog must not need the Clipboard API
    await page.evaluate(() => { Object.defineProperty(navigator, "clipboard", { value: undefined }); });
    await dlg.getByRole("radio", { name: "Under the staging root on this retnd host" }).check();
    await expect(dlg.getByRole("textbox", { name: /Name under the staging root/ })).toBeVisible();
    await dlg.getByRole("textbox", { name: /Name under the staging root/ }).fill("pg-restore-1");
    await expect(dlg.getByText("retnd recover production/postgres-primary/" + PG + " --to pg-restore-1", { exact: true })).toBeVisible();
    // the flag the CLI really has is the only spelling anywhere
    await expect(dlg).not.toContainText("--accept-cost");
  });

  test("says up front that the host prepares the download and then checks every piece it sends", async ({ page }) => {
    await toDetail(page, PG);
    await openDialog(page);
    const dlg = dialog(page);
    await expect(dlg).toContainText(/reads the whole copy against the digest it recorded/);
    await expect(dlg).toContainText(/straight from where it lives, 4 MiB at a time/);
    await expect(dlg).toContainText(/nothing is copied to the host's disk/);
    await expect(dlg).toContainText(/the download is cut off/);
    // the retired wording: a whole-copy staging that the download no longer does
    await expect(dlg).not.toContainText(/reads the whole copy and checks it against the digest it recorded before it sends the first byte/);
  });

  test("a backup with no copy has nothing to recover and the action stays off", async ({ page }) => {
    await toDetail(page, "media-week34.tar");
    await openDialog(page);
    await expect(dialog(page).getByTestId("no-copy")).toContainText(/nothing to recover/);
    await expect(primary(page)).toBeDisabled();
  });
});

test.describe("#985 Recover artifact: an archived copy is a restore, then a recovery", () => {
  test("blocks on a cost decision that names the class, shows no price and no time, and offers no browser download", async ({ page }) => {
    await toDetail(page, ARCHIVED);
    await openDialog(page);
    const dlg = dialog(page);
    const decision = dlg.getByRole("group", { name: "This copy is archived" });
    await expect(decision).toContainText("DEEP_ARCHIVE");
    await expect(decision).toContainText("offsite_cold");
    await expect(decision).toContainText(/provider bills for it/);
    await expect(decision).toContainText(/shows neither/);
    // no invented number: no currency amount and no hour estimate
    await expect(dlg).not.toContainText(/[$€£]\s?\d|\d+(\.\d+)?\s?(USD|EUR)|about \d+ hours/i);
    await expect(dlg.getByRole("radio", { name: "Download in this browser" })).toBeDisabled();
    await expect(dlg.getByTestId("no-browser-for-archive")).toContainText(/grant lasts minutes/);
    await expect(dlg.getByRole("radio", { name: "Under the staging root on this retnd host" })).toBeChecked();
    await expect(dlg).not.toContainText("--accept-cost");
    // the restore on its own is a verb the CLI has
    await expect(dlg.getByText("retnd restore production/billing-mysql/" + ARCHIVED + " --medium <id> --acknowledge", { exact: true })).toBeVisible();
  });

  test("nothing starts on Enter or Escape, and the paid action needs the box", async ({ page }) => {
    await toDetail(page, ARCHIVED);
    await openDialog(page);
    const dlg = dialog(page);
    await dlg.getByRole("textbox", { name: /Name under the staging root/ }).fill("billing-restore");
    const ask = primary(page);
    await expect(ask).toBeDisabled();
    await page.keyboard.press("Enter");
    await expect(dlg).toHaveAttribute("data-phase", "choose");
    await dlg.getByRole("checkbox", { name: /I accept that the provider bills/ }).check();
    await expect(ask).toBeEnabled();
    await page.keyboard.press("Escape");
    await expect(dlg).toHaveCount(0);
    expect((await store(page)).thaws ?? {}).toEqual({});
  });

  test("asks for the restore, watches the copy become readable, then recovers it with the digest the host placed", async ({ page }) => {
    await toDetail(page, ARCHIVED);
    await openDialog(page);
    const dlg = dialog(page);
    await dlg.getByRole("textbox", { name: /Name under the staging root/ }).fill("billing-restore");
    await dlg.getByRole("checkbox", { name: /I accept that the provider bills/ }).check();
    await primary(page).click();

    const thaw = dlg.getByTestId("thaw");
    await expect(thaw).toBeVisible();
    await expect(dlg).toHaveAttribute("data-phase", "thawing");
    await expect(thaw).toHaveAttribute("data-access", "restoring");
    await expect(thaw).toContainText(/no percentage and no finishing time/);
    await expect(thaw).toContainText("Published time for this class");
    await expect(thaw).not.toContainText(/%/);
    await expect(thaw).not.toContainText(/Recovered/);

    await expect(thaw).toHaveAttribute("data-access", "immediate", { timeout: 15_000 });
    await expect(thaw).toContainText("The provider has restored the copy");
    await dlg.getByRole("button", { name: "Recover the restored copy" }).click();
    await expect(dlg.getByTestId("placed")).toBeVisible({ timeout: 15_000 });
    await expect(dlg.getByTestId("placed-digest")).toHaveText(/^sha256:[0-9a-f]{64}$/);
    expect((await store(page)).thaws["production/billing-mysql/" + ARCHIVED]).toBeGreaterThan(0);
  });

  test("a reload mid-restore re-attaches to the restore and asks for no second one", async ({ page }) => {
    await open(page, "/backups/production/billing-mysql/" + ARCHIVED, "default");
    await page.evaluate((k) => sessionStorage.setItem(k + ".thawMs", "120000"), STORE);
    await openDialog(page);
    const dlg = dialog(page);
    await dlg.getByRole("textbox", { name: /Name under the staging root/ }).fill("billing-restore");
    await dlg.getByRole("checkbox", { name: /I accept that the provider bills/ }).check();
    await primary(page).click();
    await expect(dlg.getByTestId("thaw")).toHaveAttribute("data-access", "restoring");
    const first = (await store(page)).thaws["production/billing-mysql/" + ARCHIVED];

    await page.reload();
    await openDialog(page);
    await expect(dialog(page).getByTestId("thaw")).toHaveAttribute("data-access", "restoring");
    await expect(dialog(page).getByRole("group", { name: "This copy is archived" })).toHaveCount(0);
    await expect(dialog(page).getByRole("button", { name: "Ask the provider to restore this copy" })).toHaveCount(0);
    expect((await store(page)).thaws["production/billing-mysql/" + ARCHIVED]).toBe(first);
  });
});

test.describe("#985 Recover artifact: checking on the host", () => {
  test("a staging recovery shows the host check with no progress and says Recovered only once the host has placed it", async ({ page }) => {
    await toDetail(page, PG);
    await openDialog(page);
    const dlg = dialog(page);
    await dlg.getByRole("radio", { name: "Under the staging root on this retnd host" }).check();
    await dlg.getByRole("textbox", { name: /Name under the staging root/ }).fill("pg-restore-1");
    await primary(page).click();

    const check = dlg.getByTestId("host-check");
    await expect(check).toBeVisible();
    await expect(check).toHaveAttribute("data-phase", "verifying");
    await expect(check).toContainText("Checking the copy on the host");
    await expect(check).toContainText(/writes nothing until that matches/);
    // the host reports no progress, so none is drawn and nothing claims success yet
    await expect(check.getByRole("progressbar")).not.toHaveAttribute("aria-valuenow", /.*/);
    await expect(dlg).not.toContainText(/Recovered/);
    await expect(dlg.getByRole("button", { name: "Cancel download" })).toHaveCount(0);

    const placed = dlg.getByTestId("placed");
    await expect(placed).toBeVisible({ timeout: 10_000 });
    await expect(placed).toContainText("Recovered to staging");
    await expect(placed).toContainText("matched the whole copy to the recorded digest before it placed anything");
    await expect(placed).toContainText("/data/recover-staging/pg-restore-1");
    await expect(dlg.getByTestId("placed-digest")).toHaveText(/^sha256:[0-9a-f]{64}$/);
  });

  test("a destination recovery names the destination, not the staging root", async ({ page }) => {
    await toDetail(page, PG);
    await openDialog(page);
    const dlg = dialog(page);
    await dlg.getByRole("radio", { name: "Configured destination" }).check();
    await dlg.getByRole("combobox", { name: "Which configured destination" }).selectOption("offsite_s3");
    await expect(dlg.getByText("retnd recover production/postgres-primary/" + PG + " --to medium:offsite_s3", { exact: true })).toBeVisible();
    await primary(page).click();
    await expect(dlg.getByTestId("placed")).toContainText("Recovered to the destination", { timeout: 10_000 });
    await expect(dlg.getByTestId("placed")).toContainText("recovered/production/postgres-primary/" + PG);
  });

  test("a browser download is handed over and the page never claims the file arrived", async ({ page }) => {
    await toDetail(page, PG);
    await openDialog(page);
    const dlg = dialog(page);
    const download = page.waitForEvent("download");
    await primary(page).click();
    // the browser is handed the grant's own URL, and the bytes never pass through the page
    expect((await download).url()).toMatch(/\/api\/v1\/recoveries\/mockgrant1$/);

    const check = dlg.getByTestId("host-check");
    await expect(check).toHaveAttribute("data-phase", "handed");
    await expect(check).toContainText("Download handed to your browser");
    await expect(check).toContainText(/checking each piece against the recorded digest/);
    await expect(check).toContainText(/cut off short/);
    await expect(check).toContainText(/cannot see your browser's transfer/);
    await expect(check).toContainText(/can be resumed with a new link/);
    await expect(check).not.toContainText(/cannot be resumed|nothing has been sent yet/);
    await expect(dlg).not.toContainText(/Recovered|Download complete|your browser has the file/i);
    // the page offers the recorded digest to compare against, not a verdict
    await expect(dlg.getByTestId("recorded-digest")).toHaveText(/^sha256:[0-9a-f]{64}$/);
  });
});

test.describe("#985 Recover artifact: refusals are their own states", () => {
  type Case = [scenario: string, target: "download" | "staging", code: string, next: RegExp, retry: boolean];
  const cases: Case[] = [
    ["assurance-grant-expired", "staging", "RECOVERY_GRANT_EXPIRED", /Ask for a new one/, true],
    ["assurance-grant-used", "staging", "RECOVERY_GRANT_USED", /works once/, true],
    ["assurance-session-ended", "staging", "RECOVERY_GRANT_SESSION_ENDED", /Sign in again/, true],
    ["assurance-staging-full", "staging", "RECOVERY_NO_SPACE", /Nothing was written/, false],
    ["assurance-reads-busy", "staging", "RECOVERY_READS_BUSY", /grant is not spent/, true],
    ["assurance-corrupt-copy", "staging", "RECOVERY_LOCAL_COPY_CORRUPT", /nothing was delivered/, false],
    ["assurance-too-many-grants", "download", "RECOVERY_TOO_MANY_GRANTS", /let some of the waiting recoveries expire/, true]
  ];
  for (const [scenario, target, code, next, retry] of cases) {
    test(`${code} is shown as itself and never as a recovery`, async ({ page }) => {
      await toDetail(page, PG, scenario);
      await openDialog(page);
      const dlg = dialog(page);
      if (target === "staging") {
        await dlg.getByRole("radio", { name: "Under the staging root on this retnd host" }).check();
        await dlg.getByRole("textbox", { name: /Name under the staging root/ }).fill("pg-restore-1");
      }
      await primary(page).click();
      const refusal = dlg.getByTestId("refusal");
      await expect(refusal).toBeVisible({ timeout: 12_000 });
      await expect(refusal).toHaveAttribute("data-code", code);
      await expect(refusal).toContainText("Nothing was recovered");
      await expect(refusal).toContainText(next);
      await expect(dlg).toHaveAttribute("data-phase", "refused");
      await expect(dlg.getByTestId("placed")).toHaveCount(0);
      await expect(dlg).not.toContainText(/Recovered to|Download handed/);
      if (retry) await expect(dlg.getByRole("button", { name: "Ask for a new grant" })).toBeVisible();
    });
  }

  test("a set stored by the snapshot engine is pointed at the verb that restores it", async ({ page }) => {
    await toDetail(page, PG, "assurance-snapshot-set");
    await openDialog(page);
    await primary(page).click();
    const refusal = dialog(page).getByTestId("refusal");
    await expect(refusal).toHaveAttribute("data-code", "RECOVERY_USE_SNAPSHOT_RESTORE");
    await expect(refusal.getByText("retnd snapshot restore production/postgres-primary --to <dir>", { exact: true })).toBeVisible();
  });
});

test.describe("#985 recovery keyboard and layout", () => {
  test("the dialog is operable by keyboard, holds focus, and gives it back on Escape", async ({ page }) => {
    await toDetail(page, PG);
    const opener = page.getByRole("button", { name: "Recover artifact", exact: true });
    await opener.focus();
    await page.keyboard.press("Enter");
    const dlg = dialog(page);
    await expect(dlg).toBeVisible();
    for (let i = 0; i < 12; i++) {
      await page.keyboard.press("Tab");
      expect(await page.evaluate(() => !!document.activeElement?.closest('[role="dialog"]'))).toBe(true);
    }
    await page.keyboard.press("Escape");
    await expect(dlg).toHaveCount(0);
    await expect(opener).toBeFocused();
  });

  test("the recovery surfaces fit a narrow screen without sideways scrolling", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 800 });
    await toDetail(page, ARCHIVED);
    await openDialog(page);
    const dlg = dialog(page);
    await expect(dlg.getByRole("group", { name: "This copy is archived" })).toBeVisible();
    const box = await dlg.boundingBox();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(390);
    expect(await dlg.evaluate((e) => e.scrollWidth <= e.clientWidth)).toBe(true);
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`#985 every recovery dialog state meets contrast in the ${theme} theme`, async ({ page }) => {
    // choosing, with the archived decision
    await toDetail(page, ARCHIVED);
    await setTheme(page, theme);
    await openDialog(page);
    expect(await measureContrast(page, `recover dialog, archived decision, ${theme}`, '[role="dialog"]')).toEqual([]);
    expect(await measureBorders(page, `recover dialog controls, ${theme}`, '[role="dialog"] .input')).toEqual([]);
    // restoring
    const dlg = dialog(page);
    await dlg.getByRole("textbox", { name: /Name under the staging root/ }).fill("billing-restore");
    await dlg.getByRole("checkbox", { name: /I accept that the provider bills/ }).check();
    await primary(page).click();
    await expect(dlg.getByTestId("thaw")).toBeVisible();
    expect(await measureContrast(page, `recover dialog, restoring, ${theme}`, '[role="dialog"]')).toEqual([]);
    await page.keyboard.press("Escape");

    // checking on the host, then placed
    await open(page, "/backups", "default");
    await row(page, PG).click();
    await setTheme(page, theme);
    await openDialog(page);
    await dlg.getByRole("radio", { name: "Under the staging root on this retnd host" }).check();
    await dlg.getByRole("textbox", { name: /Name under the staging root/ }).fill("pg-restore-1");
    await primary(page).click();
    await expect(dlg.getByTestId("host-check")).toBeVisible();
    expect(await measureContrast(page, `recover dialog, checking on the host, ${theme}`, '[role="dialog"]')).toEqual([]);
    await expect(dlg.getByTestId("placed")).toBeVisible({ timeout: 10_000 });
    expect(await measureContrast(page, `recover dialog, placed, ${theme}`, '[role="dialog"]')).toEqual([]);
    await page.keyboard.press("Escape");

    // a refusal
    await toDetail(page, PG, "assurance-staging-full");
    await setTheme(page, theme);
    await openDialog(page);
    await dlg.getByRole("radio", { name: "Under the staging root on this retnd host" }).check();
    await dlg.getByRole("textbox", { name: /Name under the staging root/ }).fill("pg-restore-1");
    await primary(page).click();
    await expect(dlg.getByTestId("refusal")).toBeVisible({ timeout: 10_000 });
    expect(await measureContrast(page, `recover dialog, refusal, ${theme}`, '[role="dialog"]')).toEqual([]);
  });
}
