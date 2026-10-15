/**
 * Issue #1179, from the review of #1185: "Download in this browser" for a
 * selection is bounded by the engine's download ceiling and reports only what
 * it knows.
 *
 * The engine streams a fixed number of downloads at once (four by default, a
 * platform may set fewer), and a grant takes its slot when the browser first
 * follows it, so the one past the ceiling fails. Clicks made by the page after
 * an await have lost the person's activation, and a browser holds those
 * behind its "several downloads" prompt while a two-minute grant runs out.
 * So the page:
 *
 *   - offers "in this browser" only up to the ceiling the engine reports;
 *   - mints the links and shows them as real links the person opens, one
 *     click each, and counts what was opened rather than what was "handed";
 *   - says when the links stop working, and can make fresh ones;
 *   - starts one run at a time.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const bar = (p: Page) => p.getByRole("region", { name: "Selected backups" });
const rowBox = (p: Page, f: string) => p.getByRole("checkbox", { name: "Select " + f, exact: true });
const links = (p: Page) => p.getByTestId("bulk-download");
const radio = (p: Page) => bar(p).getByRole("radio", { name: "Download in this browser" });

const FIVE = [
  "postgres-primary-20260801.bak",
  "billing-mysql-20260802.bak",
  "auth-config-20260803.bak",
  "postgres-primary-20260804.bak",
  "billing-mysql-20260805.bak"
];

async function tick(page: Page, scenario: string, files: string[]) {
  await open(page, "/backups", scenario);
  for (const f of files) await rowBox(page, f).check();
  await expect(bar(page)).toBeVisible();
}

test.describe("#1179 the browser download is capped at the engine's ceiling", () => {
  test("a ceiling of 2 turns the option off for five backups and says why", async ({ page }) => {
    await tick(page, "backups-many-ceiling-2", FIVE);
    await expect(radio(page)).toBeDisabled();
    const note = bar(page).getByTestId("bulk-download-note");
    await expect(note).toContainText("at most 2");
    await expect(note).toContainText(/staging root|configured destination/);
    // the other two places still work for the whole selection
    await bar(page).getByRole("radio", { name: "Configured destination" }).check();
    await bar(page).getByRole("combobox", { name: "Which configured destination" }).selectOption("usb_shelf");
    await expect(bar(page).getByRole("button", { name: "Recover selected" })).toBeEnabled();
  });

  test("the same ceiling allows two", async ({ page }) => {
    await tick(page, "backups-many-ceiling-2", FIVE.slice(0, 2));
    await expect(radio(page)).toBeEnabled();
    await radio(page).check();
    await expect(bar(page).getByRole("button", { name: "Download selected" })).toBeEnabled();
  });

  test("ticking past the ceiling with the option chosen moves off it", async ({ page }) => {
    await tick(page, "backups-many-ceiling-2", FIVE.slice(0, 2));
    await radio(page).check();
    await rowBox(page, FIVE[2]).check();
    await expect(radio(page)).toBeDisabled();
    await expect(radio(page)).not.toBeChecked();
    await expect(bar(page).getByRole("button", { name: "Download selected" })).toHaveCount(0);
  });

  test("the default ceiling is the engine's four", async ({ page }) => {
    await tick(page, "backups-many", FIVE.slice(0, 4));
    await expect(radio(page)).toBeEnabled();
    await rowBox(page, FIVE[4]).check();
    await expect(radio(page)).toBeDisabled();
    await expect(bar(page).getByTestId("bulk-download-note")).toContainText("at most 4");
  });
});

test.describe("#1179 the browser download is links the person opens", () => {
  test("nothing downloads until a link is opened, and each open is counted", async ({ page }) => {
    await tick(page, "backups-many", FIVE.slice(0, 2));
    await radio(page).check();
    const urls: string[] = [];
    page.on("download", (d) => urls.push(d.url()));
    await bar(page).getByRole("button", { name: "Download selected" }).click();
    const first = links(page).getByRole("link", { name: "Download " + FIVE[0] });
    const second = links(page).getByRole("link", { name: "Download " + FIVE[1] });
    await expect(first).toBeVisible();
    await expect(second).toBeVisible();
    await expect(links(page)).toContainText("0 of 2 opened");
    expect(urls).toEqual([]);

    const d1 = page.waitForEvent("download");
    await first.click();
    expect((await d1).url()).toMatch(/\/api\/v1\/recoveries\/mockgrant\d+$/);
    await expect(links(page)).toContainText("1 of 2 opened");
    // an opened link is spent (a grant is single use), so it is not offered again
    await expect(first).toHaveCount(0);

    const d2 = page.waitForEvent("download");
    await second.click();
    await d2;
    await expect(links(page)).toContainText("2 of 2 opened");
    expect(new Set(urls).size).toBe(2);
    // the page cannot see a browser transfer, so it never says the files arrived
    await expect(links(page)).not.toContainText(/handed|downloads? complete|your browser has the files?|recovered/i);
  });

  test("says when the links stop working, and fresh ones replace them", async ({ page }) => {
    await page.clock.install();
    await tick(page, "backups-many", FIVE.slice(0, 2));
    await radio(page).check();
    await bar(page).getByRole("button", { name: "Download selected" }).click();
    const first = links(page).getByRole("link", { name: "Download " + FIVE[0] });
    await expect(first).toBeVisible();
    await expect(links(page)).toContainText(/work once and stop working at/);
    const before = await first.getAttribute("href");

    // past the grants' expiry the links are not offered as if they worked
    await page.clock.fastForward("06:00");
    await expect(links(page)).toContainText(/expired/i);
    await expect(first).toHaveCount(0);

    await links(page).getByRole("button", { name: "Make fresh links" }).click();
    await expect(first).toBeVisible();
    expect(await first.getAttribute("href")).not.toBe(before);
    await expect(links(page)).toContainText("0 of 2 opened");
  });

  test("an archived copy is left out and named, and only the others get links", async ({ page }) => {
    await tick(page, "backups-many", ["postgres-primary-20260801.bak", "billing-20260827.sql.gz"]);
    await radio(page).check();
    await bar(page).getByRole("button", { name: "Download selected" }).click();
    await expect(links(page).getByRole("link", { name: "Download postgres-primary-20260801.bak" })).toBeVisible();
    await expect(links(page).getByRole("link")).toHaveCount(1);
    await expect(links(page)).toContainText(/billing-20260827\.sql\.gz.*archived/);
    await expect(links(page)).toContainText("0 of 1 opened");
  });

  test("a second click while the links are being made starts nothing", async ({ page }) => {
    await tick(page, "backups-many", FIVE.slice(0, 2));
    await radio(page).check();
    // Both clicks in one task, before React has re-rendered the button as
    // busy: only the page's own guard can stop the second one.
    await bar(page).getByRole("button", { name: "Download selected" }).evaluate((b) => { (b as HTMLButtonElement).click(); (b as HTMLButtonElement).click(); });
    await expect(links(page).getByRole("link")).toHaveCount(2);
    const hrefs = await links(page).getByRole("link").evaluateAll((as) => as.map((a) => (a as HTMLAnchorElement).getAttribute("href")));
    expect(hrefs).toEqual(["/api/v1/recoveries/mockgrant1", "/api/v1/recoveries/mockgrant2"]);
  });

  test("the terminal echo is a runnable twin, one named file per backup", async ({ page }) => {
    await tick(page, "backups-many", FIVE.slice(0, 3));
    await radio(page).check();
    const echo = bar(page).getByTestId("recover-command");
    for (const f of FIVE.slice(0, 3)) await expect(echo).toContainText("--to - > " + f);
    await expect(echo).not.toContainText("recovered-file");
    await expect(echo).not.toContainText("# and");
  });
});
