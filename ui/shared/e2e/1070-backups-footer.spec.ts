import { expect, test } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

// Issue #1070 (EPIC Q, item 22): the Backups footer states the recovery
// boundary in the secondary ink. It used to say "retained, verified copies" in
// --text-3, which is 3.35:1 on the light surface and fails AA, and it called
// every copy verified beside chips that are careful not to.
const BOUNDARY =
  "This product recovers and exports the retained artifact; restoring it into an application remains the operator’s responsibility.";

test.describe("#1070 the Backups footer", () => {
  test("states the boundary and no longer calls every copy verified", async ({ page }) => {
    await open(page, "/backups");
    const note = page.getByTestId("backups-boundary");
    await expect(note).toBeVisible();
    await expect(note).toHaveText(BOUNDARY.replace("’", "'"));
    await expect(page.locator(".card__footer")).not.toContainText(/verified copies|does not perform application restore/);
  });

  test("is drawn in --text-2, the same ink as the count beside it", async ({ page }) => {
    await open(page, "/backups");
    const note = page.getByTestId("backups-boundary");
    const same = await page.evaluate(() => {
      const probe = document.createElement("span");
      probe.style.color = "var(--text-2)";
      document.body.appendChild(probe);
      const want = getComputedStyle(probe).color;
      probe.remove();
      return want === getComputedStyle(document.querySelector('[data-testid="backups-boundary"]')!).color;
    });
    expect(same).toBe(true);
    await expect(note).toBeVisible();
  });

  for (const theme of ["light", "dark"] as const) {
    test(`meets 4.5:1 in the ${theme} theme`, async ({ page }) => {
      await open(page, "/backups");
      await expect(page.getByTestId("backups-boundary")).toBeVisible();
      await setTheme(page, theme);
      expect(await measureContrast(page, `Backups footer, ${theme}`, ".card__footer")).toEqual([]);
    });
  }
});
