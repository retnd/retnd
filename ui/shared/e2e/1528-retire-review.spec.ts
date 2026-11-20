import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { open, setTheme } from "./helpers";

/**
 * Issue #1528, the review of the Retire dialog (#1536). Each case here was
 * written red, against the dialog as it first shipped:
 *
 *   - keyboard: focus moves into the dialog, stays there, lands on the result
 *     and goes back to whatever opened it;
 *   - with nothing newer, ticking the acknowledgement asks the engine again and
 *     shows what retiring does, with the engine's own terminal line;
 *   - the Note field and the dialog's buttons have edges a person can see;
 *   - the design's "Replaced by" choice, Retry switched off with the reason when
 *     the source is gone, the Retired state in the Backups table, the notice when
 *     retnd retired something on its own, and 44px targets;
 *   - retiring one row leaves an unrelated recovery selection alone.
 *
 * The fixtures are the mock's forge set: `retire-forge` has media-week34.tar
 * FAILED with its source gone and two newer good backups (week35, week36);
 * `retire-source-present` has the source still on the host; `retire-auto` has
 * week34 already retired by the engine on its own.
 */
const WEEK34 = "media-week34.tar";
const WEEK34_ID = "media/weekly-archive/" + WEEK34;
const BILLING = "billing-20260827.sql.gz";
const dialog = (page: Page) => page.getByRole("dialog");

async function insideDialog(page: Page): Promise<boolean> {
  return page.evaluate(() => !!document.activeElement?.closest("[role=dialog]"));
}

/** WCAG relative-luminance contrast between an element's border and the
 *  background behind it (its own fill, else the nearest painted ancestor). */
async function edgeContrast(locator: Locator): Promise<{ own: number; around: number }> {
  return locator.evaluate((el) => {
    const c = document.createElement("canvas").getContext("2d")!;
    const rgb = (s: string) => {
      c.clearRect(0, 0, 1, 1);
      c.fillStyle = "#000";
      c.fillStyle = s;
      c.fillRect(0, 0, 1, 1);
      const d = c.getImageData(0, 0, 1, 1).data;
      return [d[0], d[1], d[2], d[3] / 255];
    };
    const lum = (v: number[]) => {
      const f = (x: number) => { x /= 255; return x <= 0.03928 ? x / 12.92 : Math.pow((x + 0.055) / 1.055, 2.4); };
      return 0.2126 * f(v[0]) + 0.7152 * f(v[1]) + 0.0722 * f(v[2]);
    };
    const ratio = (a: number[], b: number[]) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
    const painted = (from: Element | null): number[] => {
      for (let e = from; e; e = e.parentElement) {
        const v = rgb(getComputedStyle(e).backgroundColor);
        if (v[3] > 0) return v;
      }
      return [255, 255, 255, 1];
    };
    const edge = rgb(getComputedStyle(el).borderTopColor);
    return { own: ratio(edge, painted(el)), around: ratio(edge, painted(el.parentElement)) };
  });
}

test.describe("#1528 review of the Retire dialog", () => {
  test("#1528 opened from the row menu, focus moves into the dialog, stays there on Tab, and Escape returns it to the menu button", async ({ page }) => {
    await open(page, "/backups", "retire-forge");
    await page.getByRole("button", { name: "Actions for " + WEEK34 }).click();
    await page.getByRole("menu").getByRole("menuitem", { name: "Retire…" }).click();
    await expect(dialog(page).getByTestId("retire-row-" + WEEK34)).toContainText("Gone from the host");
    expect(await insideDialog(page), "focus is still behind the dialog").toBe(true);
    for (let i = 0; i < 12; i++) {
      await page.keyboard.press("Tab");
      expect(await insideDialog(page), "Tab number " + (i + 1) + " left the dialog").toBe(true);
    }
    for (let i = 0; i < 4; i++) {
      await page.keyboard.press("Shift+Tab");
      expect(await insideDialog(page), "Shift+Tab left the dialog").toBe(true);
    }
    await page.keyboard.press("Escape");
    await expect(dialog(page)).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Actions for " + WEEK34 })).toBeFocused();
  });

  test("#1528 retiring from the keyboard lands focus on the result, and closing hands it back to the page", async ({ page }) => {
    await open(page, "/backups/media/weekly-archive/" + WEEK34, "retire-forge");
    await page.getByRole("button", { name: "Retire…" }).click();
    await expect(dialog(page).getByTestId("retire-row-" + WEEK34)).toContainText("Gone from the host");
    await dialog(page).getByRole("button", { name: "Retire 1 backup" }).focus();
    await page.keyboard.press("Enter");
    await expect(dialog(page).getByRole("heading", { name: "1 backup retired" })).toBeVisible();
    expect(await insideDialog(page), "focus fell out of the dialog when the result replaced the buttons").toBe(true);
    await page.keyboard.press("Escape");
    await expect(dialog(page)).toHaveCount(0);
    expect(await page.evaluate(() => document.activeElement !== document.body), "focus was dropped on the body").toBe(true);
  });

  test("#1528 with nothing newer, ticking the acknowledgement shows what happens and the engine's own terminal line", async ({ page }) => {
    await open(page, "/backups/media/weekly-archive/" + WEEK34, "backups-ingest-failed");
    await page.getByRole("button", { name: "Retire…" }).click();
    await dialog(page).getByRole("checkbox", { name: /I understand there is no replacement/ }).check();
    const happens = dialog(page).getByRole("region", { name: "What happens" });
    await expect(happens).toContainText("The backup record stays, marked RETIRED");
    await expect(dialog(page)).toContainText("retnd retire " + WEEK34_ID + " --no-successor");
  });

  for (const theme of ["light", "dark"] as const) {
    test("#1528 the Note field and the dialog's buttons have a 3:1 edge in the " + theme + " theme", async ({ page }) => {
      await open(page, "/backups/media/weekly-archive/" + WEEK34, "retire-forge");
      await setTheme(page, theme);
      await page.getByRole("button", { name: "Retire…" }).click();
      const note = dialog(page).getByLabel("Note (optional)");
      await expect(note).toBeVisible();
      const n = await edgeContrast(note);
      console.log("CONTRAST 1528 note edge " + theme + " | own " + n.own.toFixed(2) + ":1, around " + n.around.toFixed(2) + ":1");
      expect(Math.min(n.own, n.around), "the Note field's edge").toBeGreaterThanOrEqual(3);
      const select = dialog(page).getByLabel("Replaced by");
      const s = await edgeContrast(select);
      console.log("CONTRAST 1528 successor select edge " + theme + " | own " + s.own.toFixed(2) + ":1, around " + s.around.toFixed(2) + ":1");
      expect(Math.min(s.own, s.around), "the Replaced by select's edge").toBeGreaterThanOrEqual(3);
      const cancel = await edgeContrast(dialog(page).getByRole("button", { name: "Cancel" }));
      console.log("CONTRAST 1528 cancel edge " + theme + " | " + cancel.own.toFixed(2) + ":1");
      expect(cancel.own, "the Cancel button's edge").toBeGreaterThanOrEqual(3);
    });
  }

  test("#1528 Replaced by defaults to the newest good backup, and choosing another changes the line and the retirement", async ({ page }) => {
    await open(page, "/backups/media/weekly-archive/" + WEEK34, "retire-forge");
    await page.getByRole("button", { name: "Retire…" }).click();
    const select = dialog(page).getByLabel("Replaced by");
    await expect(select).toHaveValue("media-week36.tar");
    await select.selectOption("media-week35.tar");
    await expect(dialog(page)).toContainText("retnd retire " + WEEK34_ID + " --superseded-by media-week35.tar");
    await dialog(page).getByRole("button", { name: "Retire 1 backup" }).click();
    await expect(dialog(page).getByRole("status")).toContainText("replaced by media-week35.tar");
  });

  test("#1528 Retry is off with the reason when the source is gone, and on when it is still there", async ({ page }) => {
    await open(page, "/backups/media/weekly-archive/" + WEEK34, "retire-forge");
    await expect(page.getByRole("button", { name: "Retry ingestion" })).toBeDisabled();
    await expect(page.getByTestId("retry-off-reason")).toContainText("nothing left to copy");
    await open(page, "/backups/media/weekly-archive/" + WEEK34, "retire-source-present");
    await expect(page.getByRole("button", { name: "Retry ingestion" })).toBeEnabled();
  });

  test("#1528 a retired backup reads Retired in the Backups table, with what replaced it", async ({ page }) => {
    await open(page, "/backups", "retire-auto");
    const row = page.getByRole("row").filter({ hasText: WEEK34 });
    await expect(row).toContainText("Retired");
    await expect(row).toContainText("Replaced by media-week36.tar");
  });

  test("#1528 when retnd retires a backup on its own, the Backups page says so and links to it", async ({ page }) => {
    await open(page, "/backups", "retire-auto");
    const notice = page.getByTestId("auto-retired-notice");
    await expect(notice).toContainText("retnd retired 1 failed backup on its own");
    await notice.getByRole("link", { name: "See it" }).click();
    await expect(page).toHaveURL(/media-week34\.tar/);
  });

  test("#1528 the Retire controls are at least 44px tall", async ({ page }) => {
    await open(page, "/backups/media/weekly-archive/" + WEEK34, "retire-forge");
    const opener = page.getByRole("button", { name: "Retire…" });
    expect((await opener.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    await opener.click();
    for (const name of ["Cancel", "Retire 1 backup"]) {
      expect((await dialog(page).getByRole("button", { name }).boundingBox())!.height, name).toBeGreaterThanOrEqual(44);
    }
  });

  test("#1528 retiring one row from its menu leaves an unrelated recovery selection alone", async ({ page }) => {
    await open(page, "/backups", "retire-forge");
    const billing = page.getByRole("checkbox", { name: "Select " + BILLING });
    await billing.check();
    await page.getByRole("button", { name: "Actions for " + WEEK34 }).click();
    await page.getByRole("menu").getByRole("menuitem", { name: "Retire…" }).click();
    await dialog(page).getByRole("button", { name: "Retire 1 backup" }).click();
    await dialog(page).getByRole("button", { name: "Close" }).click();
    await expect(billing).toBeChecked();
  });
});
