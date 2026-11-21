import { expect, test } from "@playwright/test";
import { contrastReport, open, setTheme } from "./helpers";

/**
 * EPIC O, #900 (FR-100): the log level control in Settings, in a real
 * browser. The select saves the engine's configured floor, the line under it
 * says which level is in force and where it came from, and the terminal line
 * under that follows the saved level. Contrast is measured on the computed
 * colours in both themes, the command line included.
 */
const control = (page: import("@playwright/test").Page) => page.getByTestId("logs-level-control");
// Exact, because the tooltip carries a "Close help for <label>" button that a
// substring match would also find.
const select = (page: import("@playwright/test").Page) => page.getByLabel("Log level", { exact: true });

// Centres the control in the view, clear of the terminal dock, and holds the
// frame a moment when a video is being recorded, so the recording shows the
// state the assertions judged.
async function settle(page: import("@playwright/test").Page) {
  await control(page).scrollIntoViewIfNeeded();
  await control(page).evaluate((el) => el.scrollIntoView({ block: "center" }));
  if (process.env.E2E_VIDEO) {
    const hide = page.getByRole("button", { name: "Hide terminal" });
    if (await hide.isVisible()) await hide.click();
    await control(page).evaluate((el) => el.scrollIntoView({ block: "center" }));
    await page.waitForTimeout(900);
  }
}

test.describe("#900 log level control", () => {
  test("#900 a saved level moves the level in force and the terminal line with it", async ({ page }) => {
    await open(page, "/settings");
    await settle(page);
    await expect(select(page)).toHaveValue("info");
    await expect(page.getByTestId("logs-level-in-force")).toContainText("In force: info (from config)");
    await expect(control(page)).toContainText("retnd settings observability patch --logs-level info");

    await select(page).selectOption("warn");
    await expect(page.getByTestId("logs-level-in-force")).toContainText("In force: warn (from config)");
    await expect(control(page)).toContainText("retnd settings observability patch --logs-level warn");
    await settle(page);

    await select(page).selectOption("error");
    await expect(control(page)).toContainText("retnd settings observability patch --logs-level error");
    await expect(select(page)).toHaveValue("error");
    await settle(page);
  });

  for (const theme of ["light", "dark"] as const) {
    test("#900 text in the log level control clears WCAG AA in the " + theme + " theme, the command line included", async ({ page }) => {
      await open(page, "/settings");
      await setTheme(page, theme);
      await expect(control(page)).toContainText("--logs-level info");
      await settle(page);
      const rows = await contrastReport(page, '[data-testid="logs-level-control"]');
      expect(rows.length).toBeGreaterThan(2);
      const worst = rows.reduce((a, b) => (a.ratio - a.need < b.ratio - b.need ? a : b));
      console.log("#900 " + theme + " worst text pair " + worst.ratio.toFixed(2) + ":1 (needs " + worst.need + ") for " + JSON.stringify(worst.text.slice(0, 40)));
      const echo = rows.find((r) => r.text.startsWith("retnd settings observability patch"));
      expect(echo, "the command line under the control was not measured").toBeTruthy();
      console.log("#900 " + theme + " command line " + echo!.ratio.toFixed(2) + ":1 (" + echo!.fg + " on " + echo!.bg + ")");
      const note = rows.find((r) => r.text.includes("In force"));
      if (note) console.log("#900 " + theme + " in-force line " + note.ratio.toFixed(2) + ":1 (" + note.fg + " on " + note.bg + ")");
      for (const r of rows) expect(r.ratio, r.sel + " " + JSON.stringify(r.text)).toBeGreaterThanOrEqual(r.need);
    });
  }
});
