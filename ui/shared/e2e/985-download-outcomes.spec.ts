import { expect, test } from "@playwright/test";
import { measureContrast, measureEdges, open, setTheme } from "./helpers";

// Issue #985, item 14: how a browser download ended, read from the engine's
// feed and drawn on the artifact page. The browser's own download manager is
// the progress surface, so the page shows only the end: completed, interrupted
// at N of M bytes, or failed verification. Each is its own outcome and none of
// them is drawn as a success.
const CASES = [
  { id: "production/postgres-primary/postgres-prod-20260828.dump.zst", outcome: "completed", badge: /^Completed$/, sentence: /every chunk matched/ },
  { id: "production/billing-mysql/billing-20260827.sql.gz", outcome: "interrupted", badge: /^Interrupted at 1200 of 5000 bytes$/, sentence: /resume from byte 1200/ },
  { id: "production/auth-config/auth-config-20260826.tar.zst", outcome: "failed_verification", badge: /^Failed verification$/, sentence: /recover from another copy/ }
] as const;
const SCENARIO = "assurance-download-outcomes";

test.describe("#985 the download outcome on the artifact page", () => {
  for (const c of CASES) {
    test(`${c.outcome} is drawn as itself`, async ({ page }) => {
      await open(page, "/backups/" + c.id, SCENARIO, { wire: true });
      const box = page.getByTestId("download-outcome");
      await expect(box).toHaveAttribute("data-outcome", c.outcome);
      await expect(box.locator("[data-badge]")).toHaveText(c.badge);
      await expect(box).toContainText(c.sentence);
    });

    test(`${c.outcome} never reaches success styling`, async ({ page }) => {
      await open(page, "/backups/" + c.id, SCENARIO, { wire: true });
      const box = page.getByTestId("download-outcome");
      await expect(box).toBeVisible();
      const used = await box.evaluate((e) => {
        const ok = (() => { const p = document.createElement("i"); p.style.color = "var(--ok)"; p.style.backgroundColor = "var(--ok-quiet)"; document.body.appendChild(p); const s = getComputedStyle(p); const r = [s.color, s.backgroundColor]; p.remove(); return r; })();
        const hit: string[] = [];
        for (const n of [e, ...e.querySelectorAll("*")]) {
          const s = getComputedStyle(n);
          for (const v of [s.color, s.backgroundColor, s.borderTopColor]) if (ok.includes(v)) hit.push(n.tagName + " " + v);
        }
        return hit;
      });
      expect(used).toEqual([]);
      await expect(box).not.toContainText(/Content verified|Recovery test passed|Recovered/);
      await expect(box.locator('[data-state="pass"]')).toHaveCount(0);
    });
  }

  test("the three outcomes have three different shapes, not three hues of one", async ({ page }) => {
    const shapes: string[] = [];
    for (const c of CASES) {
      await open(page, "/backups/" + c.id, SCENARIO, { wire: true });
      shapes.push(await page.getByTestId("download-outcome").locator("[data-badge]").evaluate((e) => {
        const s = getComputedStyle(e);
        return [s.borderTopStyle, s.borderTopWidth, e.closest("[data-outcome]")!.querySelector("[data-glyph]")?.getAttribute("data-glyph")].join("|");
      }));
    }
    expect(new Set(shapes).size).toBe(3);
  });

  test("an artifact the feed says nothing about shows no outcome", async ({ page }) => {
    await open(page, "/backups/production/legacy-redis/redis-20260812.rdb.zst", SCENARIO, { wire: true });
    await expect(page.getByRole("button", { name: "Recover artifact" })).toBeVisible();
    await expect(page.getByTestId("download-outcome")).toHaveCount(0);
  });

  for (const theme of ["light", "dark"] as const) {
    test(`text meets 4.5:1 and every edge 3:1 in the ${theme} theme`, async ({ page }) => {
      for (const c of CASES) {
        await open(page, "/backups/" + c.id, SCENARIO, { wire: true });
        await expect(page.getByTestId("download-outcome")).toBeVisible();
        await setTheme(page, theme);
        expect(await measureContrast(page, `download ${c.outcome}, ${theme}`, '[data-testid="download-outcome"]')).toEqual([]);
        expect(await measureEdges(page, `download ${c.outcome}, ${theme}`, '[data-testid="download-outcome"] [data-badge]', [c.outcome])).toEqual([]);
      }
    });
  }
});
