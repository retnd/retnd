/**
 * Issue #1179: the download/export dialog.
 *
 *   - "Download in this browser" can be selected (on the Backups list's
 *     selection bar it was a radio that was always disabled);
 *   - "Folder under the staging root on the host" has a folder browser over the machine retnd
 *     runs on, not only a text field;
 *   - "Configured destination" is a drop-down of Settings, STORAGE
 *     DESTINATIONS, or says "None configured".
 *
 * The same three controls sit on the Recover artifact dialog, the selection
 * bar and the Recover set dialog, so each is held on each.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, row, measureBorders, measureContrast, setTheme } from "./helpers";

const PG = "postgres-prod-20260828.dump.zst";
const MANY = "backups-many";
const bar = (p: Page) => p.getByRole("region", { name: "Selected backups" });
const rowBox = (p: Page, f: string) => p.getByRole("checkbox", { name: "Select " + f, exact: true });
const dialog = (p: Page) => p.getByRole("dialog", { name: "Recover artifact" });
const browserOf = (scope: ReturnType<Page["locator"]>) => scope.getByRole("group", { name: "Folders on the host" });

async function singleDialog(page: Page, scenario = "default") {
  await open(page, "/backups", scenario);
  await row(page, PG).click();
  await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
  await expect(dialog(page)).toBeVisible();
  return dialog(page);
}

async function tickTwo(page: Page) {
  await open(page, "/backups", MANY);
  await rowBox(page, "postgres-primary-20260801.bak").check();
  await rowBox(page, "billing-mysql-20260802.bak").check();
  await expect(bar(page)).toBeVisible();
}

test.describe("#1179 Download in this browser", () => {
  test("is selectable on the selection bar, and then asks for no place", async ({ page }) => {
    await tickTwo(page);
    const radio = bar(page).getByRole("radio", { name: "Download in this browser" });
    await expect(radio).toBeEnabled();
    await radio.check();
    await expect(radio).toBeChecked();
    await expect(bar(page).getByRole("textbox")).toHaveCount(0);
    await expect(bar(page).getByRole("button", { name: "Download selected" })).toBeEnabled();
  });

  test("makes one link per ticked backup, and each link is one download", async ({ page }) => {
    await tickTwo(page);
    await bar(page).getByRole("radio", { name: "Download in this browser" }).check();
    const urls: string[] = [];
    page.on("download", (d) => urls.push(d.url()));
    await bar(page).getByRole("button", { name: "Download selected" }).click();
    const links = page.getByTestId("bulk-download").getByRole("link");
    await expect(links).toHaveCount(2);
    // The review of #1185: the page follows no link itself, the person does.
    expect(urls.length).toBe(0);
    for (let i = 0; i < 2; i++) {
      const d = page.waitForEvent("download");
      await links.first().click();
      await d;
    }
    expect(new Set(urls).size).toBe(2);
    await expect(page.getByTestId("bulk-download")).toContainText("2 of 2 opened");
    // the page cannot see a browser transfer, so it never says the files arrived
    await expect(page.getByTestId("bulk-download")).not.toContainText(/downloads? complete|your browser has the files?|recovered/i);
  });

  test("an archived copy is left out and the page says so", async ({ page }) => {
    await open(page, "/backups", MANY);
    await rowBox(page, "postgres-primary-20260801.bak").check();
    await rowBox(page, "billing-20260827.sql.gz").check();
    await bar(page).getByRole("radio", { name: "Download in this browser" }).check();
    await bar(page).getByRole("button", { name: "Download selected" }).click();
    await expect(page.getByTestId("bulk-download")).toContainText("0 of 1 opened");
    await expect(page.getByTestId("bulk-download")).toContainText(/billing-20260827\.sql\.gz.*archived/);
    await expect(page.getByTestId("bulk-download").getByRole("link")).toHaveCount(1);
  });

  test("stays selectable on the Recover artifact dialog and downloads", async ({ page }) => {
    const dlg = await singleDialog(page);
    const radio = dlg.getByRole("radio", { name: "Download in this browser" });
    await expect(radio).toBeEnabled();
    // #1191 made the set's source host the default, so the download is chosen.
    await radio.check();
    await expect(radio).toBeChecked();
    const download = page.waitForEvent("download");
    await dlg.getByRole("button", { name: "Recover artifact", exact: true }).click();
    expect((await download).url()).toMatch(/\/api\/v1\/recoveries\/mockgrant1$/);
  });
});

test.describe("#1179 Folder under the staging root on the host: a folder browser", () => {
  for (const where of ["dialog", "bar"] as const) {
    test(`${where}: browses the host's folders and puts the chosen one in the field`, async ({ page }) => {
      let scope;
      if (where === "dialog") scope = await singleDialog(page);
      else { await tickTwo(page); scope = bar(page); }
      await scope.getByRole("radio", { name: "Folder under the staging root on the host" }).check();
      const browser = browserOf(scope);
      await expect(browser).toBeVisible();
      await expect(browser.getByTestId("browser-root")).toContainText("/var/lib/retnd/recovery-staging");
      for (const f of ["daily", "pg-restores", "weekly"]) {
        await expect(browser.getByRole("button", { name: "Open folder " + f })).toBeVisible();
      }
      await browser.getByRole("button", { name: "Open folder daily" }).click();
      await browser.getByRole("button", { name: "Open folder 2026-10" }).click();
      await expect(browser.getByTestId("browser-path")).toHaveText("daily/2026-10");
      await browser.getByRole("button", { name: "Use this folder" }).click();
      // One backup's target is folder/name (the last part is the file the host
      // writes), so the dialog offers the folder plus the backup's own file
      // name. A selection takes a folder and so gets the folder alone.
      await expect(scope.getByRole("textbox", { name: /Name under the staging root/ })).toHaveValue(where === "dialog" ? "daily/2026-10/" + PG : "daily/2026-10");
      // "Up" walks back out, and is not offered at the root
      await browser.getByRole("button", { name: "Up one folder" }).click();
      await expect(browser.getByTestId("browser-path")).toHaveText("daily");
      await browser.getByRole("button", { name: "Up one folder" }).click();
      await expect(browser.getByRole("button", { name: "Up one folder" })).toBeDisabled();
    });
  }

  test("an empty folder says so", async ({ page }) => {
    const dlg = await singleDialog(page);
    await dlg.getByRole("radio", { name: "Folder under the staging root on the host" }).check();
    const browser = browserOf(dlg);
    await browser.getByRole("button", { name: "Open folder pg-restores" }).click();
    await expect(browser).toContainText("No folders in here");
  });

  test("works from the keyboard", async ({ page }) => {
    const dlg = await singleDialog(page);
    await dlg.getByRole("radio", { name: "Folder under the staging root on the host" }).check();
    const open_ = browserOf(dlg).getByRole("button", { name: "Open folder weekly" });
    await open_.focus();
    await page.keyboard.press("Enter");
    await expect(browserOf(dlg).getByTestId("browser-path")).toHaveText("weekly");
  });

  test("a host that cannot list its folders says so and leaves the text field working", async ({ page }) => {
    const dlg = await singleDialog(page, "host-browse-unavailable");
    await dlg.getByRole("radio", { name: "Folder under the staging root on the host" }).check();
    await expect(browserOf(dlg).getByRole("alert")).toContainText("cannot list its folders");
    const field = dlg.getByRole("textbox", { name: /Name under the staging root/ });
    await field.fill("typed-by-hand");
    await expect(dlg.getByRole("button", { name: "Recover artifact", exact: true })).toBeEnabled();
  });

  test("the typed name still reaches the command echo", async ({ page }) => {
    const dlg = await singleDialog(page);
    await dlg.getByRole("radio", { name: "Folder under the staging root on the host" }).check();
    await browserOf(dlg).getByRole("button", { name: "Open folder weekly" }).click();
    await browserOf(dlg).getByRole("button", { name: "Use this folder" }).click();
    await expect(dlg.getByText("--to weekly/" + PG, { exact: false })).toBeVisible();
  });
});

test.describe("#1179 Configured destination: a drop-down of Settings", () => {
  test("lists every destination Settings lists, and choosing one fills the command", async ({ page }) => {
    const dlg = await singleDialog(page);
    await dlg.getByRole("radio", { name: "Configured destination" }).check();
    const select = dlg.getByRole("combobox", { name: "Configured destination" });
    await expect(select).toBeVisible();
    await expect(dlg.getByRole("textbox", { name: "Recover to destination id" })).toHaveCount(0);
    // the destinations are read once the dialog is up, so wait for them
    await expect(select.locator("option")).toHaveCount(5);
    const values = await select.locator("option").evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value));
    expect(values).toEqual(["", "local", "usb_shelf", "offsite_s3", "offsite_cold"]);
    await select.selectOption("offsite_s3");
    await expect(dlg.getByText("--to medium:offsite_s3", { exact: false })).toBeVisible();
    await expect(dlg.getByRole("button", { name: "Recover artifact", exact: true })).toBeEnabled();
  });

  test("nothing is chosen until one is, so the button waits", async ({ page }) => {
    const dlg = await singleDialog(page);
    await dlg.getByRole("radio", { name: "Configured destination" }).check();
    await expect(dlg.getByRole("button", { name: "Recover artifact", exact: true })).toBeDisabled();
  });

  test("says None configured when Settings has no destination", async ({ page }) => {
    const dlg = await singleDialog(page, "no-destinations");
    await dlg.getByRole("radio", { name: "Configured destination" }).check();
    const select = dlg.getByRole("combobox", { name: "Configured destination" });
    await expect(select).toBeDisabled();
    await expect(select).toContainText("None configured");
    await expect(dlg.getByRole("button", { name: "Recover artifact", exact: true })).toBeDisabled();
  });

  test("the selection bar has the same drop-down", async ({ page }) => {
    await tickTwo(page);
    await bar(page).getByRole("radio", { name: "Configured destination" }).check();
    const select = bar(page).getByRole("combobox", { name: "Configured destination" });
    await select.selectOption("usb_shelf");
    await expect(bar(page).getByTestId("recover-command")).toContainText("medium:usb_shelf");
  });
});

test.describe("#1179 contrast and edges, both themes", () => {
  for (const theme of ["light", "dark"] as const) {
    test(`the new controls meet WCAG AA in ${theme}`, async ({ page }) => {
      const dlg = await singleDialog(page);
      await setTheme(page, theme);
      await dlg.getByRole("radio", { name: "Folder under the staging root on the host" }).check();
      await expect(browserOf(dlg)).toBeVisible();
      await browserOf(dlg).getByRole("button", { name: "Open folder daily" }).click();
      // measure the folder that has been listed: while it is being asked for
      // every button is disabled, and a disabled control has no edge to measure
      await expect(browserOf(dlg).getByRole("button", { name: "Open folder 2026-10" })).toBeVisible();
      expect(await measureContrast(page, `folder browser, ${theme}`, '[role="group"][aria-label="Folders on the host"]')).toEqual([]);
      expect(await measureBorders(page, `folder browser controls, ${theme}`, '[role="group"][aria-label="Folders on the host"] .btn')).toEqual([]);
      await dlg.getByRole("radio", { name: "Configured destination" }).check();
      await expect(dlg.getByRole("combobox", { name: "Configured destination" })).toBeEnabled();
      expect(await measureContrast(page, `destination drop-down, ${theme}`, '[role="dialog"]')).toEqual([]);
      expect(await measureBorders(page, `destination drop-down edge, ${theme}`, '[role="dialog"] select')).toEqual([]);
    });

    test(`the bar's download state meets WCAG AA in ${theme}`, async ({ page }) => {
      await tickTwo(page);
      await setTheme(page, theme);
      await bar(page).getByRole("radio", { name: "Download in this browser" }).check();
      await bar(page).getByRole("button", { name: "Download selected" }).click();
      await expect(page.getByTestId("bulk-download")).toContainText("0 of 2 opened", { timeout: 15_000 });
      await expect(page.getByTestId("bulk-download").getByRole("link")).toHaveCount(2);
      expect(await measureContrast(page, `selection bar, download, ${theme}`, '[data-testid="selection-bar"]')).toEqual([]);
      expect(await measureContrast(page, `bulk download status, ${theme}`, '[data-testid="bulk-download"]')).toEqual([]);
      // the review of #1185: the links and "Make fresh links" sit on the amber notice
      expect(await measureBorders(page, `bulk download button edge, ${theme}`, '[data-testid="bulk-download"] .btn')).toEqual([]);
    });
  }
});

test.describe("#1179 Recover this backup set has the same two controls", () => {
  const setDialog = (p: Page) => p.getByRole("dialog", { name: "Recover this backup set" });
  async function openSet(page: Page) {
    await open(page, "/sets/production/postgres-primary", "assurance-set-recover");
    await page.getByRole("button", { name: "Recover this backup set", exact: true }).click();
    await expect(setDialog(page)).toBeVisible();
  }

  test("a folder browser fills the folder name", async ({ page }) => {
    await openSet(page);
    const dlg = setDialog(page);
    // #1191: the source host is the default; this is about the staging folder.
    await dlg.getByRole("radio", { name: "Folder under the staging root on the host" }).check();
    const browser = browserOf(dlg);
    await expect(browser).toBeVisible();
    await browser.getByRole("button", { name: "Open folder weekly" }).click();
    await browser.getByRole("button", { name: "Use this folder" }).click();
    await expect(dlg.getByRole("textbox", { name: /Name under the staging root/ })).toHaveValue("weekly");
  });

  test("a drop-down of Settings fills the destination", async ({ page }) => {
    await openSet(page);
    const dlg = setDialog(page);
    await dlg.getByRole("radio", { name: "Configured destination" }).check();
    await dlg.getByRole("combobox", { name: "Configured destination" }).selectOption("offsite_s3");
    await expect(dlg.getByText("--to medium:offsite_s3", { exact: false }).first()).toBeVisible();
  });
});
