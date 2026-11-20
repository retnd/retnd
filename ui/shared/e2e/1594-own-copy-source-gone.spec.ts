import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1594: a FAILED backup whose own copy sits at its final name (#662's
 * shape) and whose source is gone could never leave FAILED. The backup page
 * switched Retry off ("nothing left to copy"), the Retire dialog refused with
 * "Retry instead", and nothing overrode either.
 *
 * What the browser owes now (docs/design/1594-own-copy-source-gone.md):
 *
 *   - Retry stays on for this shape, because retry keeps a copy that still
 *     matches the checksum recorded when it was verified, without the source;
 *   - a copy that does not verify is refused by retry with the engine's own
 *     sentence, which names the way out;
 *   - the Retire dialog offers the unverified-copy acknowledgement, keeps the
 *     file, and refuses a copy that does verify with Retry instead.
 *
 * The fixtures are the mock's forge set with media-week34.tar's own copy at
 * its final name and its source gone: `own-copy-verified` (the copy matches)
 * and `own-copy-unverified` (it does not). media-week35 and 36 are newer good
 * backups, so a retirement has a replacement to name.
 */
const WEEK34 = "media-week34.tar";
const WEEK34_ID = "media/weekly-archive/" + WEEK34;
const PAGE = "/backups/media/weekly-archive/" + WEEK34;
const dialog = (page: Page) => page.getByRole("dialog");
const recovery = (page: Page) => page.getByTestId("recovery-card");
/** A recording (E2E_VIDEO) keeps the last state on screen long enough to read
 *  in the GIF; an ordinary run does not wait. */
const hold = async (page: Page) => {
  if (process.env.E2E_VIDEO) await page.waitForTimeout(1500);
};
/** Opens the Retire dialog and moves the pointer off the button, so the
 *  button's own tooltip does not sit over the dialog. */
async function openRetire(page: Page) {
  await page.getByRole("button", { name: "Retire…" }).click();
  await page.mouse.move(2, 2);
}

test.describe("#1594 a failed backup with its own copy at the final name and its source gone", () => {
  test("#1594 Retry stays on, says it keeps the copy, and keeping it takes the backup out of FAILED", async ({ page }) => {
    await open(page, PAGE, "own-copy-verified");
    const retry = page.getByRole("button", { name: "Retry ingestion" });
    await expect(retry).toBeEnabled();
    await expect(page.getByTestId("retry-off-reason")).toHaveCount(0);
    await expect(recovery(page)).toContainText("its own copy is already at its final name");
    await retry.click();
    await expect(page.getByRole("button", { name: "Retry ingestion" })).toHaveCount(0);
    await expect(recovery(page)).toHaveCount(0);
    await hold(page);
  });

  test("#1594 a copy that does not verify is refused by Retry with the engine's way out", async ({ page }) => {
    await open(page, PAGE, "own-copy-unverified");
    await page.getByRole("button", { name: "Retry ingestion" }).click();
    const said = recovery(page).getByRole("status");
    await expect(said).toContainText("could not be verified");
    await expect(said).toContainText("--accept-unverified-copy");
    await expect(page.getByRole("button", { name: "Retry ingestion" })).toBeVisible();
    await said.scrollIntoViewIfNeeded();
    await hold(page);
  });

  test("#1594 the Retire dialog asks for the unverified-copy acknowledgement, keeps the file and retires it", async ({ page }) => {
    await open(page, PAGE, "own-copy-unverified");
    await openRetire(page);
    const row = dialog(page).getByTestId("retire-row-" + WEEK34);
    await expect(row).toContainText("Gone from the host");
    await expect(dialog(page)).toContainText("its own copy is at its final name");
    const retire = dialog(page).getByRole("button", { name: /^Retire/ }).last();
    await expect(retire).toBeDisabled();

    await dialog(page).getByRole("checkbox", { name: /could not be verified/ }).check();
    const happens = dialog(page).getByRole("region", { name: "What happens" });
    await expect(happens).toContainText("is kept where it is");
    await expect(dialog(page)).toContainText("retnd retire " + WEEK34_ID + " --accept-unverified-copy");

    await dialog(page).getByRole("button", { name: "Retire 1 backup" }).click();
    await expect(dialog(page).getByRole("heading", { name: "1 backup retired" })).toBeVisible();
    await expect(dialog(page).getByRole("status")).toContainText("replaced by media-week36.tar");
    await hold(page);
  });

  test("#1594 a copy that verifies is never retired: the acknowledgement turns into Retry instead", async ({ page }) => {
    await open(page, PAGE, "own-copy-verified");
    await openRetire(page);
    await dialog(page).getByRole("checkbox", { name: /could not be verified/ }).check();
    const row = dialog(page).getByTestId("retire-row-" + WEEK34);
    await expect(row).toContainText("still matches");
    await expect(row.getByRole("button", { name: "Retry instead" })).toBeVisible();
    await expect(dialog(page).getByRole("button", { name: /^Retire/ }).last()).toBeDisabled();
    await hold(page);
    await row.getByRole("button", { name: "Retry instead" }).click();
    await expect(recovery(page)).toHaveCount(0);
    await hold(page);
  });

  for (const theme of ["light", "dark"] as const) {
    test("#1594 the Recovery card and the acknowledgement meet WCAG AA in the " + theme + " theme", async ({ page }) => {
      await open(page, PAGE, "own-copy-unverified");
      await setTheme(page, theme);
      await expect(recovery(page)).toContainText("its own copy is already at its final name");
      expect(await measureContrast(page, "1594 recovery card " + theme, "[data-testid=recovery-card]"), "pairs under AA").toEqual([]);
      await page.getByRole("button", { name: "Retry ingestion" }).click();
      await expect(recovery(page).getByRole("status")).toContainText("could not be verified");
      expect(await measureContrast(page, "1594 retry refusal " + theme, "[data-testid=recovery-card] [role=status]"), "pairs under AA").toEqual([]);
      await page.getByRole("button", { name: "Retire…" }).click();
      await expect(dialog(page)).toContainText("its own copy is at its final name");
      expect(await measureContrast(page, "1594 dialog unverified " + theme, "[role=dialog]"), "pairs under AA").toEqual([]);
      await dialog(page).getByRole("checkbox", { name: /could not be verified/ }).check();
      await expect(dialog(page).getByRole("region", { name: "What happens" })).toContainText("is kept where it is");
      expect(await measureContrast(page, "1594 dialog acknowledged " + theme, "[role=dialog]"), "pairs under AA").toEqual([]);
    });
  }
});
