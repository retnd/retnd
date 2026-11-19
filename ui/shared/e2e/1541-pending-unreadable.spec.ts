/**
 * Issue #1541: a source file that is complete but not readable yet (the
 * producer has not published it) used to be journaled and FAILED nineteen
 * seconds later, and the set read FAILING for two weeks with a good backup
 * every morning. Now it waits, and the set card says so beside the state,
 * without the set being called failing.
 *
 * What I want on the card: nothing when no file is waiting; a quiet line with
 * the count, the oldest path and its age while the oldest is inside the stale
 * threshold; the engine's warning on the warn tint once it is not; the health
 * badge unchanged either way; and a tooltip saying what it means. Every text
 * pair at 4.5:1 and the readout's edge and icon at 3:1, in both themes.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureBorders, measureContrast, open, setTheme } from "./helpers";

const card = (p: Page, name: string) => p.locator("article.card").filter({ hasText: name });
const readout = (p: Page, name: string) => card(p, name).getByTestId("pending-unreadable");

async function opened(page: Page, scenario = "pending-unreadable") {
  await open(page, "/sets", scenario);
  await expect(card(page, "Production PostgreSQL")).toBeVisible();
}

test.describe("#1541 complete but unreadable source files on the set card", () => {
  test("#1541 a file waiting past the stale threshold shows the engine's warning", async ({ page }) => {
    await opened(page);
    const r = readout(page, "Production PostgreSQL");
    await expect(r).toBeVisible();
    await expect(r).toHaveAttribute("data-warned", "true");
    await expect(r).toContainText("Not published yet.");
    await expect(r).toContainText(
      "1 source file complete but unreadable for 30h, oldest run-20261103T033855Z/gitea-dump-20261103T033855Z.tar.gz; the producer has not published it"
    );
  });

  test("#1541 the set stays healthy while a file waits for its producer", async ({ page }) => {
    await opened(page);
    await expect(card(page, "Production PostgreSQL").getByText("Healthy", { exact: true })).toBeVisible();
    await expect(card(page, "Production PostgreSQL").getByText(/Failing/i)).toHaveCount(0);
  });

  test("#1541 a file inside the stale threshold is a quiet count, not a warning", async ({ page }) => {
    await opened(page);
    const r = readout(page, "Billing MySQL");
    await expect(r).toHaveAttribute("data-warned", "false");
    await expect(r).toContainText("2 complete source files not readable yet, the oldest billing-20260829.sql.gz for 2h.");
  });

  test("#1541 a set with nothing waiting shows no readout", async ({ page }) => {
    await opened(page);
    await expect(readout(page, "Auth service config")).toHaveCount(0);
    await opened(page, "default");
    await expect(page.getByTestId("pending-unreadable")).toHaveCount(0);
  });

  test("#1541 the readout's tooltip says it never changes the health state", async ({ page }) => {
    await opened(page);
    await page.evaluate(() => window.localStorage.setItem("retnd.tooltips", "on"));
    await page.reload();
    await readout(page, "Production PostgreSQL").hover();
    await expect(page.locator(".tooltip__pop:visible").filter({ hasText: "It never changes the set's health state." })).toHaveCount(1);
  });

  test("#1541 a long path wraps inside the card at a phone width", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await opened(page);
    const r = readout(page, "Production PostgreSQL");
    await expect(r).toBeVisible();
    const fits = await r.evaluate((el) => {
      const box = el.getBoundingClientRect();
      const host = el.closest("article")!.getBoundingClientRect();
      return box.right <= host.right + 1 && el.scrollWidth <= el.clientWidth + 1;
    });
    expect(fits).toBe(true);
  });

  for (const theme of ["light", "dark"] as const) {
    test("#1541 the readout reads at AA in the " + theme + " theme", async ({ page }) => {
      await opened(page);
      await setTheme(page, theme);
      for (const name of ["Production PostgreSQL", "Billing MySQL"]) {
        const sel = 'article.card:has-text("' + name + '") [data-testid="pending-unreadable"]';
        expect(await measureContrast(page, "1541 " + name + " " + theme, sel), name + " text " + theme).toEqual([]);
      }
      const warned = 'article.card:has-text("Production PostgreSQL") [data-testid="pending-unreadable"]';
      expect(await measureBorders(page, "1541 warned edge " + theme, warned), "warned edge " + theme).toEqual([]);
      const icon = await page.locator(warned + " svg").evaluate((svg) => {
        const lum = (c: string) => {
          const [r, g, b] = (c.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number).map((v) => {
            const s = v / 255;
            return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
          });
          return 0.2126 * r + 0.7152 * g + 0.0722 * b;
        };
        const ink = document.createElement("canvas").getContext("2d")!;
        const rgb = (c: string) => {
          ink.fillStyle = c;
          ink.fillRect(0, 0, 1, 1);
          const d = ink.getImageData(0, 0, 1, 1).data;
          return "rgb(" + d[0] + "," + d[1] + "," + d[2] + ")";
        };
        const fg = rgb(getComputedStyle(svg).color);
        const bg = rgb(getComputedStyle(svg.closest("[data-testid='pending-unreadable']")!).backgroundColor);
        const [a, b] = [lum(fg), lum(bg)].sort((x, y) => y - x);
        return { fg, bg, ratio: (a + 0.05) / (b + 0.05) };
      });
      console.log("ICON 1541 warned " + theme + " | " + icon.ratio.toFixed(2) + ":1 (needs 3) | " + icon.fg + " on " + icon.bg);
      expect(icon.ratio, "warning icon " + theme).toBeGreaterThanOrEqual(3);
    });
  }
});
