/**
 * Issue #1107: select backups on the Backups list, including everything a
 * filter matches, and recover them to one destination.
 *
 * The scenario `backups-many` lists 34 backups: thirty generated ones (one per
 * day from 2026-08-01, rotating postgres-primary, billing-mysql, auth-config)
 * and the four fixtures. Of those, one (media-week34.tar) has no copy at all
 * and so is not selectable; one (billing-20260827.sql.gz) is archived and is
 * selectable but costs money. 33 rows are selectable.
 *
 * Decision under test: changing a filter CLEARS the selection. The list is
 * refetched per filter, so a hidden selected row would be one the page can no
 * longer show, size or name, and "37 selected, 12 not in the filter" would be
 * a sentence about rows nobody can see. Clearing means a selection is only
 * ever rows currently on screen, and the page says when it did so.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, measureBorders, open, setTheme } from "./helpers";

const MANY = "backups-many";
/** A row checkbox: "Select <file>", never the header's "Select all matching backups". */
const ROW = /^Select (?!all matching)/;
const bar = (page: Page) => page.getByRole("region", { name: "Selected backups" });
const count = (page: Page) => page.getByTestId("selection-count");
const rowBox = (page: Page, filename: string) => page.getByRole("checkbox", { name: "Select " + filename, exact: true });
const headerBox = (page: Page) => page.getByRole("checkbox", { name: "Select all matching backups" });
const progress = (page: Page) => page.getByRole("dialog", { name: "Recovering backups" });

async function chooseStaging(page: Page, name: string) {
  await bar(page).getByRole("radio", { name: "Folder under the staging root on the host" }).check();
  await bar(page).getByRole("textbox", { name: /Name under the staging root/ }).fill(name);
}

test.describe("#1107 selecting backups", () => {
  test("every row has a checkbox named for its artifact, and a row that cannot be recovered says why", async ({ page }) => {
    await open(page, "/backups", MANY);
    await expect(rowBox(page, "postgres-prod-20260828.dump.zst")).toBeVisible();
    await expect(page.getByRole("checkbox", { name: ROW })).toHaveCount(34);
    const none = rowBox(page, "media-week34.tar");
    await expect(none).toBeDisabled();
    // the reason is attached to the control, not only drawn beside it
    await expect(none).toHaveAccessibleDescription(/no copy/i);
    await expect(rowBox(page, "billing-20260827.sql.gz")).toBeEnabled();
  });

  test("nothing is selected at first, so there is no bar", async ({ page }) => {
    await open(page, "/backups", MANY);
    await expect(bar(page)).toHaveCount(0);
  });

  test("ticking rows shows a count and a total size, announced politely", async ({ page }) => {
    await open(page, "/backups", MANY);
    await rowBox(page, "postgres-primary-20260801.bak").check();
    await rowBox(page, "billing-mysql-20260802.bak").check();
    await expect(bar(page)).toBeVisible();
    await expect(count(page)).toContainText("2 selected");
    // 1 MB + 2 MB
    await expect(bar(page)).toContainText(/2\.9 MB/);
    await expect(count(page)).toHaveAttribute("role", "status");
    await expect(count(page)).toHaveAttribute("aria-live", "polite");
    await rowBox(page, "billing-mysql-20260802.bak").uncheck();
    await expect(count(page)).toContainText("1 selected");
  });

  test("ticking a box does not open the backup", async ({ page }) => {
    await open(page, "/backups", MANY);
    await rowBox(page, "postgres-primary-20260801.bak").check();
    await expect(page).toHaveURL(/\/backups(\?|$)/);
  });

  test("works from the keyboard: Tab to a box, Space ticks it", async ({ page }) => {
    await open(page, "/backups", MANY);
    const box = rowBox(page, "postgres-primary-20260801.bak");
    await box.focus();
    await page.keyboard.press("Space");
    await expect(box).toBeChecked();
    await expect(count(page)).toContainText("1 selected");
    await expect(page).toHaveURL(/\/backups(\?|$)/);
    await page.keyboard.press("Space");
    await expect(box).not.toBeChecked();
  });

  test("the header box selects every selectable row, skips the unrecoverable one, and is mixed when partial", async ({ page }) => {
    await open(page, "/backups", MANY);
    await rowBox(page, "postgres-primary-20260801.bak").check();
    await expect(headerBox(page)).toHaveAttribute("aria-checked", "mixed");
    await headerBox(page).check();
    await expect(count(page)).toContainText("33 selected");
    await expect(count(page)).toContainText("of 33 matching");
    await expect(rowBox(page, "media-week34.tar")).not.toBeChecked();
    await headerBox(page).uncheck();
    await expect(bar(page)).toHaveCount(0);
  });

  test("the bar offers 'Select all N matching' while only some are ticked, and Clear selection empties it", async ({ page }) => {
    await open(page, "/backups", MANY);
    await rowBox(page, "postgres-primary-20260801.bak").check();
    await bar(page).getByRole("button", { name: "Select all 33 matching" }).click();
    await expect(count(page)).toContainText("33 selected");
    await expect(bar(page).getByRole("button", { name: /Select all/ })).toHaveCount(0);
    await bar(page).getByRole("button", { name: "Clear selection" }).click();
    await expect(bar(page)).toHaveCount(0);
  });
});

test.describe("#1107 selection and filters", () => {
  test("select all after a set filter selects exactly that set's rows, and the count is what is recovered", async ({ page }) => {
    await open(page, "/backups", MANY);
    await page.getByRole("combobox", { name: "Filter by backup set" }).selectOption({ label: "Billing MySQL" });
    // ten generated + the archived fixture
    await expect(page.getByRole("checkbox", { name: /^Select .*\.(bak|gz)$/ })).toHaveCount(11);
    await headerBox(page).check();
    await expect(count(page)).toContainText("11 selected");
    await expect(count(page)).toContainText("of 11 matching");
    for (const n of await page.getByRole("checkbox", { name: ROW }).all()) await expect(n).toBeChecked();
    // the printed command names exactly those 11 and nothing else
    const cmd = await bar(page).getByTestId("recover-command").innerText();
    expect(cmd.match(/production\/billing-mysql\//g)?.length).toBe(11);
    expect(cmd).not.toContain("postgres-primary");
    expect(cmd).not.toContain("auth-config");
  });

  test("received-after and received-before narrow the list, and select all takes only those rows", async ({ page }) => {
    await open(page, "/backups", MANY);
    await page.getByLabel("Received on or after").fill("2026-08-20");
    await page.getByLabel("Received on or before").fill("2026-08-24");
    await expect(page.getByRole("checkbox", { name: ROW })).toHaveCount(5);
    await headerBox(page).check();
    await expect(count(page)).toContainText("5 selected");
    const cmd = await bar(page).getByTestId("recover-command").innerText();
    expect(cmd.match(/\.bak/g)?.length).toBe(5);
    for (const d of ["20", "21", "22", "23", "24"]) expect(cmd).toContain("202608" + d);
    expect(cmd).not.toContain("20260825");
  });

  test("changing a filter with a live selection clears it, and says so", async ({ page }) => {
    await open(page, "/backups", MANY);
    await headerBox(page).check();
    await expect(count(page)).toContainText("33 selected");
    await page.getByRole("combobox", { name: "Filter by backup set" }).selectOption({ label: "Auth service config" });
    await expect(bar(page)).toHaveCount(0);
    await expect(page.getByTestId("selection-notice")).toContainText(/selection was cleared because the filter changed/i);
    await expect(page.getByTestId("selection-notice")).toHaveAttribute("role", "status");
    for (const n of await page.getByRole("checkbox", { name: ROW }).all()) await expect(n).not.toBeChecked();
  });

  test("a date filter change also clears it", async ({ page }) => {
    await open(page, "/backups", MANY);
    await headerBox(page).check();
    await page.getByLabel("Received on or after").fill("2026-08-10");
    await expect(bar(page)).toHaveCount(0);
    await expect(page.getByTestId("selection-notice")).toBeVisible();
  });
});

test.describe("#1107 recovering a selection", () => {
  test("recovers every selected backup to one staging name, one at a time, with a summary", async ({ page }) => {
    await open(page, "/backups", MANY);
    await rowBox(page, "postgres-primary-20260801.bak").check();
    await rowBox(page, "billing-mysql-20260802.bak").check();
    await rowBox(page, "auth-config-20260803.bak").check();
    await expect(bar(page).getByRole("button", { name: "Recover selected" })).toBeDisabled();
    await chooseStaging(page, "may-drill");
    await expect(bar(page).getByTestId("recover-command")).toHaveText(
      "retnd recover production/postgres-primary/postgres-primary-20260801.bak production/billing-mysql/billing-mysql-20260802.bak production/auth-config/auth-config-20260803.bak --to may-drill"
    );
    await bar(page).getByRole("button", { name: "Recover selected" }).click();
    const dlg = progress(page);
    await expect(dlg).toBeVisible();
    await expect(dlg.getByTestId("run-summary")).toContainText("3 recovered", { timeout: 15_000 });
    await expect(dlg.getByTestId("run-summary")).toContainText("0 failed");
    const items = dlg.getByRole("listitem");
    await expect(items).toHaveCount(3);
    for (const it of await items.all()) await expect(it).toHaveAttribute("data-state", "recovered");
    // each file shows the digest the host placed
    await expect(dlg.getByTestId("placed-digest").first()).toHaveText(/^sha256:[0-9a-f]{64}$/);
  });

  // #1179 supersedes the earlier decision here: the radio was always disabled
  // ("a browser download is not offered for several files"). It is selectable
  // now, one link per backup up to the engine's download ceiling, and the bar
  // says what that means.
  test("a browser download is offered for several files, and the bar says what it does", async ({ page }) => {
    await open(page, "/backups", MANY);
    await rowBox(page, "postgres-primary-20260801.bak").check();
    const radio = bar(page).getByRole("radio", { name: "Download in this browser" });
    await expect(radio).toBeEnabled();
    await radio.check();
    await expect(bar(page).getByTestId("bulk-download-note")).toContainText(/one link per backup/i);
    await expect(bar(page).getByTestId("bulk-download-note")).toContainText(/archived copy is left out/i);
  });

  test("one corrupt copy is reported failed with its reason, and the rest still arrive", async ({ page }) => {
    await open(page, "/backups", "backups-many-corrupt");
    await page.getByLabel("Received on or after").fill("2026-08-03");
    await page.getByLabel("Received on or before").fill("2026-08-07");
    await headerBox(page).check();
    await expect(count(page)).toContainText("5 selected");
    await chooseStaging(page, "drill-2");
    await bar(page).getByRole("button", { name: "Recover selected" }).click();
    const dlg = progress(page);
    await expect(dlg.getByTestId("run-summary")).toContainText("4 recovered", { timeout: 15_000 });
    await expect(dlg.getByTestId("run-summary")).toContainText("1 failed");
    const bad = dlg.locator("li.runitem").filter({ hasText: "billing-mysql-20260805.bak" });
    await expect(bad).toHaveAttribute("data-state", "failed");
    await expect(bad).toContainText(/digest mismatch/);
    await expect(dlg.locator('[data-state="recovered"]')).toHaveCount(4);
    await expect(dlg.getByRole("alert")).toContainText("billing-mysql-20260805.bak");
  });

  test("a refusal before byte one names the numbers, recovers nothing and shows no per-file lines as done", async ({ page }) => {
    await open(page, "/backups", "backups-many-nospace");
    await rowBox(page, "postgres-primary-20260801.bak").check();
    await rowBox(page, "billing-mysql-20260802.bak").check();
    await chooseStaging(page, "too-big");
    await bar(page).getByRole("button", { name: "Recover selected" }).click();
    const dlg = progress(page);
    await expect(dlg.getByTestId("run-refusal")).toContainText("the staging disk has 1000000 bytes free");
    await expect(dlg.getByTestId("run-refusal")).toContainText("nothing was read");
    await expect(dlg.locator('[data-state="recovered"]')).toHaveCount(0);
    await expect(dlg.getByTestId("run-summary")).toHaveCount(0);
  });

  test("cancel stops the files not yet started and says how many were left", async ({ page }) => {
    await open(page, "/backups", MANY);
    await headerBox(page).check();
    await chooseStaging(page, "drill-3");
    // the archived fixture is in the full list, so the one cost box is needed
    await bar(page).getByRole("checkbox", { name: /I accept that the provider bills/ }).check();
    await bar(page).getByRole("button", { name: "Recover selected" }).click();
    const dlg = progress(page);
    await expect(dlg.getByTestId("run-progress")).toContainText(/\d+ of 33/);
    await dlg.getByRole("button", { name: "Cancel remaining" }).click();
    const summary = dlg.getByTestId("run-summary");
    await expect(summary).toContainText(/cancelled/i, { timeout: 15_000 });
    await expect(summary).toContainText(/not started/);
    const done = await dlg.locator('[data-state="recovered"], [data-state="restoring"]').count();
    const skipped = await dlg.locator('[data-state="cancelled"]').count();
    expect(skipped).toBeGreaterThan(0);
    expect(done + skipped).toBe(33);
  });

  test("an archived copy in the selection needs one cost acknowledgement for the whole set, before anything starts", async ({ page }) => {
    await open(page, "/backups", MANY);
    await rowBox(page, "billing-20260827.sql.gz").check();
    await rowBox(page, "postgres-primary-20260801.bak").check();
    await chooseStaging(page, "drill-4");
    const go = bar(page).getByRole("button", { name: "Recover selected" });
    await expect(go).toBeDisabled();
    const ack = bar(page).getByRole("checkbox", { name: /I accept that the provider bills/ });
    await expect(bar(page).getByTestId("archived-decision")).toContainText(/1 of the 2 selected/);
    await ack.check();
    await expect(go).toBeEnabled();
    await expect(bar(page).getByTestId("recover-command")).toContainText("--acknowledge-cost");
    await go.click();
    const dlg = progress(page);
    await expect(dlg.getByTestId("run-summary")).toContainText(/1 recovered/, { timeout: 15_000 });
    await expect(dlg.getByRole("listitem").filter({ hasText: "billing-20260827.sql.gz" })).toHaveAttribute("data-state", "restoring");
  });
});

test.describe("#1107 contrast", () => {
  for (const theme of ["light", "dark"] as const) {
    test("selected row, checkbox and action bar meet AA in the " + theme + " theme", async ({ page }) => {
      await open(page, "/backups", MANY);
      await setTheme(page, theme);
      await rowBox(page, "postgres-primary-20260801.bak").check();
      await rowBox(page, "billing-mysql-20260802.bak").check();
      await chooseStaging(page, "contrast-drill");
      // enabled now, so the primary button's ink is measured too
      await expect(bar(page).getByRole("button", { name: "Recover selected" })).toBeEnabled();
      expect(await measureContrast(page, theme + " date filter labels", "label:has(input[type=date])")).toEqual([]);
      const selectedRow = "tr[data-selected='true']";
      expect(await measureContrast(page, theme + " selected row", selectedRow)).toEqual([]);
      expect(await measureContrast(page, theme + " action bar", "[aria-label='Selected backups']")).toEqual([]);
      // UI components need 3:1 against what they sit on
      const ui = await page.evaluate(() => {
        const q = (s: string) => document.querySelector(s) as HTMLElement;
        const cs = (e: HTMLElement) => getComputedStyle(e);
        const box = q("tr[data-selected='true'] input[type=checkbox]");
        const c = document.createElement("canvas").getContext("2d")!;
        const rgb = (v: string) => { c.fillStyle = "#000"; c.fillStyle = v; c.fillRect(0, 0, 1, 1); const d = c.getImageData(0, 0, 1, 1).data; return [d[0], d[1], d[2]]; };
        const lum = (a: number[]) => { const f = (v: number) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }; return 0.2126 * f(a[0]) + 0.7152 * f(a[1]) + 0.0722 * f(a[2]); };
        const ratio = (a: number[], b: number[]) => (Math.max(lum(a), lum(b)) + 0.05) / (Math.min(lum(a), lum(b)) + 0.05);
        const row = box.closest("tr") as HTMLElement;
        const accent = rgb(cs(box).accentColor === "auto" ? cs(box).color : cs(box).accentColor);
        const rowBg = rgb(cs(row).backgroundColor);
        const barEl = q("[aria-label='Selected backups']");
        const barBg = rgb(cs(barEl).backgroundColor);
        const barEdge = rgb(cs(barEl).borderTopColor);
        const pageBg = rgb(cs(document.body).backgroundColor);
        return { checkboxOnRow: ratio(accent, rowBg), barEdgeOnPage: ratio(barEdge, pageBg), barOnPage: ratio(barBg, pageBg) };
      });
      console.log("CONTRAST " + theme + " checkbox accent on selected row " + ui.checkboxOnRow.toFixed(2) + ":1 (needs 3)");
      console.log("CONTRAST " + theme + " bar border on page " + ui.barEdgeOnPage.toFixed(2) + ":1 (needs 3)");
      expect(ui.checkboxOnRow).toBeGreaterThanOrEqual(3);
      expect(ui.barEdgeOnPage).toBeGreaterThanOrEqual(3);
      expect(await measureBorders(page, theme + " bar fields", "[aria-label='Selected backups'] input.input, input[type=date]")).toEqual([]);
    });
  }
});

test.describe("#1107 contrast of the progress dialog", () => {
  for (const theme of ["light", "dark"] as const) {
    test("a failed line, a recovered line and the summary meet AA in the " + theme + " theme", async ({ page }) => {
      await open(page, "/backups", "backups-many-corrupt");
      await setTheme(page, theme);
      await page.getByLabel("Received on or after").fill("2026-08-03");
      await page.getByLabel("Received on or before").fill("2026-08-07");
      await headerBox(page).check();
      await chooseStaging(page, "contrast-run");
      await bar(page).getByRole("button", { name: "Recover selected" }).click();
      await expect(progress(page).getByTestId("run-summary")).toBeVisible({ timeout: 15_000 });
      expect(await measureContrast(page, theme + " progress dialog", "[role=dialog]")).toEqual([]);
    });
  }
});
