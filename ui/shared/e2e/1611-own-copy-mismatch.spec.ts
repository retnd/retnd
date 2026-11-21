import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1611: a FAILED backup whose own copy sits at its final name, whose
 * source is still on the host, and whose copy does not match it. Retry used to
 * send the row round the pipeline into the collision guard, and the backup
 * came back FAILED with nothing said. Retry now refuses by name
 * (RETRY_OWN_COPY_MISMATCH) and the page shows the engine's sentence, which
 * says to move the file aside (it is kept) and retry.
 * See docs/design/1611-own-copy-mismatch-source-present.md.
 *
 * The fixture is the mock's forge set with media-week34.tar's own copy at its
 * final name, its source present and the two different (`own-copy-mismatch`).
 */
const PAGE = "/backups/media/weekly-archive/media-week34.tar";
const dialog = (page: Page) => page.getByRole("dialog");
const recovery = (page: Page) => page.getByTestId("recovery-card");
const hold = async (page: Page) => {
  if (process.env.E2E_VIDEO) await page.waitForTimeout(1500);
};

test.describe("#1611 a failed backup with its own copy at the final name and its source still there", () => {
  test("#1611 the Recovery card says retry compares the copy with the source and what a mismatch does", async ({ page }) => {
    await open(page, PAGE, "own-copy-mismatch");
    await expect(recovery(page)).toContainText("its own copy is already at its final name");
    await expect(recovery(page)).toContainText("still on the host");
    await expect(recovery(page)).toContainText("never overwritten");
    await expect(page.getByRole("button", { name: "Retry ingestion" })).toBeEnabled();
    await hold(page);
  });

  test("#1611 a mismatching copy is refused by Retry with the way out, and Retry stays available", async ({ page }) => {
    await open(page, PAGE, "own-copy-mismatch");
    await page.getByRole("button", { name: "Retry ingestion" }).click();
    const said = recovery(page).getByRole("status");
    await expect(said).toContainText("does not match");
    await expect(said).toContainText("Move that file aside");
    await expect(said).toContainText("do not track it");
    await expect(page.getByRole("button", { name: "Retry ingestion" })).toBeVisible();
    await said.scrollIntoViewIfNeeded();
    await hold(page);
  });

  test("#1611 the Retire dialog does not promise that retry keeps the copy", async ({ page }) => {
    await open(page, PAGE, "own-copy-mismatch");
    await page.getByRole("button", { name: "Retire…" }).click();
    await page.mouse.move(2, 2);
    await expect(dialog(page)).toContainText("keeps it if they match");
    await expect(dialog(page)).toContainText("If they do not match");
    await hold(page);
  });

  for (const theme of ["light", "dark"] as const) {
    test("#1611 the Recovery card and the refusal meet WCAG AA in the " + theme + " theme", async ({ page }) => {
      await open(page, PAGE, "own-copy-mismatch");
      await setTheme(page, theme);
      await expect(recovery(page)).toContainText("never overwritten");
      expect(await measureContrast(page, "1611 recovery card " + theme, "[data-testid=recovery-card]"), "pairs under AA").toEqual([]);
      await page.getByRole("button", { name: "Retry ingestion" }).click();
      await expect(recovery(page).getByRole("status")).toContainText("Move that file aside");
      expect(await measureContrast(page, "1611 retry refusal " + theme, "[data-testid=recovery-card] [role=status]"), "pairs under AA").toEqual([]);
    });
  }
});
