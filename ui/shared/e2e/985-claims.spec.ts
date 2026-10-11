import { expect, test } from "@playwright/test";
import { open, row, contrastFailures } from "./helpers";

// Issue #985 (Q2.2): the three claims, and the states that are not a pass.
// Each backup carries Stored, Content verified and Recovery tested as three
// separate elements, and every state renders as itself.

const claim = (r: ReturnType<typeof row>, which: "stored" | "content" | "recovery") =>
  r.locator(`[data-testid="assurance-claims"] [data-claim="${which}"]`);

test.describe("#985 the three claims on the Backups list", () => {
  test("every row has three separate claim elements and no single tick", async ({ page }) => {
    await open(page, "/backups");
    const rows = page.getByRole("row").filter({ has: page.locator('[data-testid="assurance-claims"]') });
    await expect(rows).toHaveCount(6);
    for (const r of await rows.all()) {
      await expect(r.locator("[data-claim]")).toHaveCount(3);
      await expect(claim(r, "stored")).toContainText("Stored");
      await expect(claim(r, "content")).toContainText(/Content|Existence|Not configured|Never run|Overdue|Skipped|Could not|Unknown|Archived|No readable/);
      await expect(claim(r, "recovery")).toBeVisible();
    }
  });

  test("each not-a-pass state renders as itself, not as a pass", async ({ page }) => {
    await open(page, "/backups");
    const expected: Array<[string, "content" | "recovery", string, RegExp]> = [
      ["postgres-prod-20260828.dump.zst", "content", "pass", /Content verified.*ago/],
      ["postgres-prod-20260828.dump.zst", "recovery", "pass", /Recovery test passed.*ago/],
      ["billing-20260827.sql.gz", "content", "overdue", /Overdue/],
      ["billing-20260827.sql.gz", "recovery", "never_run", /Never run/],
      ["auth-config-20260826.tar.zst", "content", "failed", /Content check failed/],
      ["auth-config-20260826.tar.zst", "recovery", "never_configured", /Not configured/],
      ["billing-20260824.sql.gz", "content", "refused_for_cost", /Skipped: read would cost egress/],
      ["billing-20260824.sql.gz", "recovery", "could_not_complete", /Could not complete/],
      ["media-week34.tar", "content", "requires_restore", /Archived: thaw needed/],
      ["redis-20260812.rdb.zst", "content", "no_readable_copy", /No readable copy/]
    ];
    for (const [file, which, state, text] of expected) {
      const c = claim(row(page, file), which);
      await expect(c, `${file} ${which}`).toHaveAttribute("data-state", state);
      await expect(c).toContainText(text);
      if (state !== "pass") await expect(c).not.toContainText(/Content verified|Recovery test passed/);
    }
    // five not-a-pass renderings are visibly different from one another
    const seen = new Set<string>();
    for (const [file, which, state] of expected) {
      if (state === "pass") continue;
      seen.add(await claim(row(page, file), which).locator("[data-chip]").evaluate((e) => {
        const s = getComputedStyle(e);
        return [s.borderTopStyle, s.borderTopWidth, e.querySelector("[data-glyph]")?.textContent].join("|");
      }) + "|" + state);
    }
    const shapes = new Set([...seen].map((s) => s.split("|").slice(0, 3).join("|")));
    expect(shapes.size).toBeGreaterThanOrEqual(7);
  });

  test("a verified checksum with a failed recovery test reads as two facts", async ({ page }) => {
    await open(page, "/backups", "assurance-split");
    const r = row(page, "postgres-prod-20260828.dump.zst");
    await expect(claim(r, "content")).toHaveAttribute("data-state", "pass");
    await expect(claim(r, "recovery")).toHaveAttribute("data-state", "failed");
    await expect(claim(r, "recovery")).toContainText("Recovery test failed");
  });

  test("an existence-only check is never rendered as content verification", async ({ page }) => {
    await open(page, "/backups", "assurance-existence");
    const c = claim(row(page, "postgres-prod-20260828.dump.zst"), "content");
    await expect(c).toHaveAttribute("data-state", "existence_only");
    await expect(c).toContainText("Existence checked only");
    await expect(c).not.toContainText("Content verified");
  });

  test("a rebuilt catalog is unknown everywhere and no path leads from unknown to pass", async ({ page }) => {
    await open(page, "/backups", "assurance-rebuilt");
    await expect(page.locator('[data-claim="content"][data-state="unknown"]')).toHaveCount(6);
    await expect(page.locator('[data-claim="recovery"][data-state="unknown"]')).toHaveCount(6);
    await expect(page.locator('[data-state="pass"]')).toHaveCount(0);
    await expect(page.getByText(/Content verified/)).toHaveCount(0);
  });

  test("an assurance API that is absent renders unknown, never a tick", async ({ page }) => {
    await open(page, "/backups", "assurance-unavailable");
    await expect(page.locator('[data-claim="content"][data-state="unknown"]')).toHaveCount(6);
    await expect(page.locator('[data-state="pass"]')).toHaveCount(0);
  });
});

test.describe("#985 the Backup detail page", () => {
  test("carries the same three claims, with its own ages, and the recover action", async ({ page }) => {
    await open(page, "/backups");
    await row(page, "postgres-prod-20260828.dump.zst").click();
    const claims = page.locator('main [data-testid="assurance-claims"]');
    await expect(claims.locator("[data-claim]")).toHaveCount(3);
    await expect(claims.locator('[data-claim="content"]')).toContainText(/3d ago/);
    await expect(claims.locator('[data-claim="recovery"]')).toContainText(/12d ago/);
    await expect(page.getByRole("button", { name: "Recover artifact" })).toBeVisible();
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`#985 claim chips meet 4.5:1 text contrast in the ${theme} theme`, async ({ page }) => {
    await open(page, "/backups");
    await expect(page.locator('[data-claim]').first()).toBeVisible();
    await page.evaluate((t) => document.documentElement.setAttribute("data-theme", t), theme);
    expect(await contrastFailures(page, '[data-testid="assurance-claims"]')).toEqual([]);
  });
}
