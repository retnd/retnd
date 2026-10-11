import { expect, test } from "@playwright/test";
import { open, row, measureContrast, setTheme } from "./helpers";

// Issue #985 (Q2.2): the three claims, and the states that are not a pass.
// Each backup carries Stored, Content verified and Recovery tested as three
// separate elements, and every state renders as itself.

const claim = (r: ReturnType<typeof row>, which: "stored" | "content" | "recovery") =>
  r.locator(`[data-testid="assurance-claims"] [data-claim="${which}"]`);

test.describe("#985 the three claims on the Backups list", () => {
  test("every row has three separate claim elements and no single tick", async ({ page }) => {
    await open(page, "/backups");
    const rows = page.getByRole("row").filter({ has: page.locator('[data-testid="assurance-claims"]') });
    await expect(rows).toHaveCount(4);
    for (const r of await rows.all()) {
      await expect(r.locator("[data-claim]")).toHaveCount(3);
      await expect(claim(r, "stored")).toContainText("Stored");
      await expect(claim(r, "content")).toContainText(/Content|Existence|Not configured|Never run|Overdue|Skipped|Could not|Unknown|Archived|No readable/);
      await expect(claim(r, "recovery")).toBeVisible();
    }
  });

  test("each not-a-pass state renders as itself, not as a pass", async ({ page }) => {
    await open(page, "/backups");
    type Row = [string, "content" | "recovery", string, RegExp];
    const main: Row[] = [
      ["postgres-prod-20260828.dump.zst", "content", "pass", /Content verified.*ago/],
      ["postgres-prod-20260828.dump.zst", "recovery", "pass", /Recovery test passed.*ago/],
      ["billing-20260827.sql.gz", "content", "requires_restore", /Archived: thaw needed/],
      ["billing-20260827.sql.gz", "recovery", "could_not_complete", /Could not complete/],
      ["media-week34.tar", "content", "never_run", /Never run/],
      ["media-week34.tar", "recovery", "never_configured", /Not configured/],
      ["redis-20260812.rdb.zst", "content", "no_readable_copy", /No readable copy/],
      ["redis-20260812.rdb.zst", "recovery", "never_configured", /Not configured/]
    ];
    const more: Row[] = [
      ["postgres-prod-20260828.dump.zst", "content", "refused_for_cost", /Skipped: read would cost egress/],
      ["postgres-prod-20260828.dump.zst", "recovery", "cleanup_failed", /Cleanup failed: staged copy left behind/],
      ["billing-20260827.sql.gz", "content", "failed", /Content check failed/],
      ["redis-20260812.rdb.zst", "content", "overdue", /Overdue/],
      ["redis-20260812.rdb.zst", "recovery", "never_run", /Never run/]
    ];
    const seen = new Set<string>();
    const done = new Set<string>();
    for (const [scenario, expected] of [["default", main], ["assurance-more", more]] as Array<[string, Row[]]>) {
      await open(page, "/backups", scenario);
      for (const [file, which, state, text] of expected) {
        const c = claim(row(page, file), which);
        await expect(c, `${file} ${which}`).toHaveAttribute("data-state", state);
        await expect(c).toContainText(text);
        if (state !== "pass") await expect(c).not.toContainText(/Content verified|Recovery test passed/);
        if (state !== "pass" && !done.has(state)) {
          done.add(state);
          seen.add(await c.locator("[data-chip]").evaluate((e) => {
            const s = getComputedStyle(e);
            return [s.borderTopStyle, s.borderTopWidth, e.querySelector("[data-glyph]")?.getAttribute("data-glyph")].join("|");
          }) + "#" + state);
        }
      }
    }
    // the not-a-pass renderings are visibly different from one another:
    // each state has its own (border style, width, glyph) triple
    const shapes = new Set([...seen].map((x) => x.split("#")[0]));
    const states = new Set([...seen].map((x) => x.split("#")[1]));
    expect(states.size).toBe(9);
    expect(shapes.size).toBe(states.size);
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
    await expect(page.locator('[data-claim="content"][data-state="unknown"]')).toHaveCount(4);
    await expect(page.locator('[data-claim="recovery"][data-state="unknown"]')).toHaveCount(4);
    await expect(page.locator('[data-state="pass"]')).toHaveCount(0);
    await expect(page.locator('[data-testid="assurance-claims"]').getByText(/Content verified/)).toHaveCount(0);
  });

  test("an assurance API that is absent renders unknown, never a tick", async ({ page }) => {
    await open(page, "/backups", "assurance-unavailable");
    await expect(page.locator('[data-claim="content"][data-state="unknown"]')).toHaveCount(4);
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
    for (const scenario of ["default", "assurance-more"]) {
      await open(page, "/backups", scenario);
      await expect(page.locator('[data-claim]').first()).toBeVisible();
      await setTheme(page, theme);
      expect(await measureContrast(page, `claim chips, ${scenario}, ${theme}`, '[data-testid="assurance-claims"]')).toEqual([]);
    }
  });
}

test.describe("#985 the Dashboard counters", () => {
  test("unknown is due, and a rebuilt catalog is never counted as nothing due", async ({ page }) => {
    await open(page, "/", "assurance-rebuilt");
    const card = page.getByRole("region", { name: "Assurance summary" });
    await expect(card.getByTestId("assurance-due")).toContainText("4");
    await expect(card.getByTestId("assurance-degraded")).not.toContainText("unknown");
  });

  test("the default fixture reports what is due and what is degraded", async ({ page }) => {
    await open(page, "/");
    const card = page.getByRole("region", { name: "Assurance summary" });
    // media-week34 has never been checked; billing's recovery test could not
    // complete and redis has no readable copy
    await expect(card.getByTestId("assurance-due")).toContainText("1");
    await expect(card.getByTestId("assurance-overdue")).toContainText("0");
    await expect(card.getByTestId("assurance-degraded")).toContainText("2");
  });

  test("an overdue recovery test makes a set due and overdue even though its content check is fresh", async ({ page }) => {
    await open(page, "/", "assurance-recovery-overdue");
    const card = page.getByRole("region", { name: "Assurance summary" });
    await expect(card.getByTestId("assurance-due")).toContainText("2");
    await expect(card.getByTestId("assurance-overdue")).toContainText("1");
  });
});
