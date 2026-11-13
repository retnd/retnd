/**
 * Issue #1351 (EPIC U, U2.3): every text pair on the Agents card, in both
 * themes, measured in the browser from the computed colours. Normal text needs
 * 4.5:1 and a component edge 3:1. The printed CONTRAST and BORDER lines are
 * what the pull request quotes; scripts/contrast/check-ui-pairs.py measures the
 * same pairs from the tokens without a browser.
 *
 * WRITTEN, NOT RUN: no browser was launched for this change. When it is run,
 * it goes through /Users/rom/workspace/xdg-pw-lock.sh and nowhere else, one
 * run at a time.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureBorders, measureContrast, setTheme } from "./helpers";
import { PASSWORD, card, openAgents, openMint, pending, region, tokenRow } from "./1351-agents";

const EDGES = ".agchip, .ag-row, .ag-strip, .input--strong-edge, .btn--strong-edge, .ag-input-error";

async function bothThemes(page: Page, label: string, selector: string) {
  for (const theme of ["light", "dark"] as const) {
    await setTheme(page, theme);
    expect(await measureContrast(page, "1351 " + label + " " + theme, selector), label + " text " + theme).toEqual([]);
    expect(await measureBorders(page, "1351 " + label + " " + theme, selector + " " + EDGES.split(", ").join(", " + selector + " ")), label + " edges " + theme).toEqual([]);
  }
}

test.describe("#1351 contrast, light and dark", () => {
  test("#1351 the tokens region, the scope list and the transport line", async ({ page }) => {
    await openAgents(page);
    await card(page).locator("[data-agents-scopes] > summary").click();
    await bothThemes(page, "tokens", "[data-agents-card]");
  });

  test("#1351 the empty states", async ({ page }) => {
    await openAgents(page, "default");
    await bothThemes(page, "empty", "[data-agents-card]");
  });

  test("#1351 pending rows, the urgent row, the step-up and a wrong password", async ({ page }) => {
    await openAgents(page, "agents-pending");
    const row = pending(page, "cf_01");
    await row.getByRole("button", { name: /^Approve (?!and run)/ }).click();
    await row.getByLabel("Your password").fill("not-the-password");
    await row.getByRole("button", { name: "Approve and run" }).click();
    await expect(row.locator("[data-approve-error]")).toBeVisible();
    await bothThemes(page, "pending", "[data-agents-card]");
  });

  test("#1351 the gate's result strip", async ({ page }) => {
    await openAgents(page, "agents-pending-gate");
    const row = pending(page, "cf_01");
    await row.getByRole("button", { name: /^Approve (?!and run)/ }).click();
    await row.getByLabel("Your password").fill(PASSWORD);
    await row.getByRole("button", { name: "Approve and run" }).click();
    await expect(card(page).locator("[data-result-strip]")).toBeVisible();
    await bothThemes(page, "gate strip", "[data-result-strip]");
  });

  test("#1351 the audit, its details and its filter refusal", async ({ page }) => {
    await openAgents(page);
    await region(page, "audit").getByRole("button", { name: /^Details of activity/ }).click();
    const since = region(page, "audit").getByLabel("Since", { exact: true });
    await since.fill("yesterday");
    await since.press("Enter");
    await expect(region(page, "audit").locator("[data-audit-error]")).toBeVisible();
    await bothThemes(page, "audit", "[data-agents-region='audit']");
  });

  test("#1351 the mint form with a refusal, and the shown-once dialog", async ({ page }) => {
    await openAgents(page, "default");
    let dlg = await openMint(page);
    await dlg.getByRole("button", { name: "Allow more than reading" }).click();
    await dlg.getByLabel("Name", { exact: true }).fill("contrast");
    await dlg.getByLabel("Expires in", { exact: true }).fill("400d");
    await dlg.getByRole("button", { name: "Create token" }).click();
    await expect(dlg.locator('[data-mint-error="expiry"]')).toBeVisible();
    await bothThemes(page, "mint form", "[role='dialog']");
    await dlg.getByLabel("Expires in", { exact: true }).fill("90d");
    await dlg.getByRole("button", { name: "Create token" }).click();
    dlg = page.getByRole("dialog", { name: /^Your token for/ });
    await expect(dlg).toBeVisible();
    await bothThemes(page, "shown once", "[role='dialog']");
  });

  test("#1351 the revoke dialog and a revoked row", async ({ page }) => {
    await openAgents(page);
    await tokenRow(page, "nightly-triage").getByRole("button", { name: "Revoke agent:nightly-triage" }).click();
    await bothThemes(page, "revoke", "[role='dialog']");
    await page.getByRole("dialog").getByRole("button", { name: "Revoke agent:nightly-triage" }).click();
    await expect(tokenRow(page, "nightly-triage")).toHaveAttribute("data-status", "revoked");
    await bothThemes(page, "revoked", "[data-agents-region='tokens']");
  });

  test("#1351 the narrow layout keeps every pair and drops no column name", async ({ page }) => {
    // Narrow for the card means its own container under 640px (agents.css). The app shell keeps a 206px sidebar, so
    // at a 360px window the card gets 47px and the page's other cards overflow it (#1337); 760px puts the card's
    // container at about 500px, which is the layout this row is about, inside a shell that can hold it.
    await page.setViewportSize({ width: 760, height: 800 });
    await openAgents(page, "agents-pending");
    await expect(tokenRow(page, "nightly-triage").locator("td[data-label='Expires']")).toBeVisible();
    // Nothing of the card pokes out of the column it lives in (the visually hidden header row is clipped, not drawn).
    expect(
      await page.evaluate(() => {
        const limit = document.querySelector("main")!.getBoundingClientRect().right + 1;
        const card = document.querySelector("[data-agents-card]")!;
        return [card, ...Array.from(card.querySelectorAll("*"))]
          .filter((e) => !e.closest("thead"))
          .every((e) => {
            const r = e.getBoundingClientRect();
            return r.width <= 1 || r.right <= limit;
          });
      })
    ).toBe(true);
    await bothThemes(page, "narrow", "[data-agents-card]");
  });
});
