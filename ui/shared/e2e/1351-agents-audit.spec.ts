/**
 * Issue #1351 (EPIC U, U2.3): the audit table. Design:
 * docs/design/1350-agents.html sections 7 and 8.
 *
 * WRITTEN, NOT RUN: no browser was launched for this change. When it is run,
 * it goes through /Users/rom/workspace/xdg-pw-lock.sh and nowhere else, one
 * run at a time.
 */
import { expect, test } from "@playwright/test";
import { card, openAgents, region } from "./1351-agents";

const rows = (page: Parameters<typeof region>[0]) => region(page, "audit").locator("tbody tr[data-audit-row]");

test.describe("#1351 the audit table", () => {
  test("#1351 has the six columns the design names, newest first, reads included", async ({ page }) => {
    await openAgents(page);
    const heads = (await region(page, "audit").locator("thead th").allTextContents()).map((h) => h.trim());
    expect(heads.slice(0, 6)).toEqual(["Time", "Token", "Tool", "Needs", "Outcome", "Correlation id"]);
    await expect(rows(page)).toHaveCount(8);
    await expect(rows(page).first()).toContainText("status");
    // Reads are rows like any other: a 200 chip.
    await expect(rows(page).first().locator('[data-chip="outcome"]')).toHaveText("200");
  });

  test("#1351 draws a success, a call waiting for a person and a refusal as different chips, none by colour alone", async ({ page }) => {
    await openAgents(page);
    const chip = (tool: string, n = 0) => region(page, "audit").locator('tr[data-tool="' + tool + '"] [data-chip="outcome"]').nth(n);
    // The success and refusal marks are the design system's icon artwork,
    // the waiting one a clock glyph: three different marks beside the words.
    await expect(chip("status")).toHaveText("200");
    await expect(chip("status").locator(".agchip__glyph svg")).toHaveCount(1);
    await expect(chip("run_cycle", 0)).toContainText("◷");
    await expect(chip("run_cycle", 0)).toContainText("AGENT_CONFIRMATION_REQUIRED");
    const refused = region(page, "audit").locator('[data-chip="outcome"][data-state="refused"]').first();
    await expect(refused.locator(".agchip__glyph svg")).toHaveCount(1);
    await expect(refused).toContainText("AGENT_SCOPE_INSUFFICIENT");
    // A read token that tried to change something is a refusal chip like any other: no extra alarm.
    const border = (c: ReturnType<typeof chip>) => c.evaluate((e) => getComputedStyle(e).borderTopWidth);
    expect(await border(refused)).toBe(await border(chip("status")));
  });

  test("#1351 names the token from the engine's token_name and the scope the tool needs from the catalogue, and echoes the audit twin", async ({ page }) => {
    await openAgents(page);
    const run = region(page, "audit").locator('tr[data-tool="run_cycle"]').first();
    await expect(run.locator("td[data-label='Token']")).toHaveText("agent:recovery-drill");
    await expect(run.locator("td[data-label='Needs']")).toHaveText("operate");
    await expect(region(page, "audit").locator('tr[data-tool="status"]').first().locator("td[data-label='Needs']")).toHaveText("read");
    await expect(region(page, "audit")).toContainText("retnd agent audit");
  });

  test("#1351 has no exit-code column, no total and no page number", async ({ page }) => {
    await openAgents(page);
    await expect(region(page, "audit")).not.toContainText(/exit code|total|page \d/i);
    await expect(region(page, "audit").getByRole("columnheader", { name: /exit/i })).toHaveCount(0);
  });

  test("#1351 Show older appends the next page and the rows already there do not move", async ({ page }) => {
    await openAgents(page, "agents-audit-many");
    await expect(rows(page)).toHaveCount(25);
    const firstTime = await rows(page).first().getAttribute("data-audit-row");
    const olderButton = region(page, "audit").getByRole("button", { name: "Show older" });
    await olderButton.scrollIntoViewIfNeeded();
    await olderButton.click();
    await expect(rows(page)).toHaveCount(50);
    expect(await rows(page).first().getAttribute("data-audit-row")).toBe(firstTime);
    await olderButton.click();
    await expect(rows(page)).toHaveCount(60);
    await expect(olderButton).toHaveCount(0);
  });

  test("#1351 states that the trail is bounded without saying how many days", async ({ page }) => {
    await openAgents(page);
    await expect(region(page, "audit")).toContainText("The trail is bounded: older rows are pruned.");
    await expect(region(page, "audit")).not.toContainText(/\b\d+ days\b/);
  });
});

test.describe("#1351 the filters", () => {
  test("#1351 Token narrows the table to one token's calls, and All tokens brings the rest back", async ({ page }) => {
    await openAgents(page);
    await region(page, "audit").getByLabel("Token", { exact: true }).selectOption({ label: "agent:recovery-drill" });
    await expect(rows(page)).toHaveCount(2);
    for (const cell of await rows(page).locator("td:nth-child(2)").allInnerTexts()) expect(cell).toBe("agent:recovery-drill");
    await region(page, "audit").getByLabel("Token", { exact: true }).selectOption({ label: "All tokens" });
    await expect(rows(page)).toHaveCount(8);
  });

  test("#1351 Since asks for the last stretch of time", async ({ page }) => {
    await openAgents(page);
    const since = region(page, "audit").getByLabel("Since", { exact: true });
    await since.fill("40m");
    await since.press("Enter");
    await expect(rows(page)).toHaveCount(5);
  });

  test("#1351 a filter that matches nothing says so and offers Clear filters, which is a different fact from empty", async ({ page }) => {
    await openAgents(page);
    const since = region(page, "audit").getByLabel("Since", { exact: true });
    await since.fill("1m");
    await since.press("Enter");
    const empty = card(page).locator('[data-agents-empty="audit-filter"]');
    await expect(empty).toContainText("No calls match this filter.");
    await expect(card(page).locator('[data-agents-empty="audit"]')).toHaveCount(0);
    await empty.getByRole("button", { name: "Clear filters" }).click();
    await expect(rows(page)).toHaveCount(8);
    await expect(since).toHaveValue("");
  });

  test("#1351 a since the engine refuses is shown as the engine wrote it", async ({ page }) => {
    await openAgents(page);
    const since = region(page, "audit").getByLabel("Since", { exact: true });
    await since.fill("yesterday");
    await since.press("Enter");
    await expect(region(page, "audit")).toContainText("since must be a duration such as 24h or 7d.");
  });
});

test.describe("#1351 Details", () => {
  test("#1351 expands the transport and the arguments, as inert text an agent supplied", async ({ page }) => {
    await openAgents(page);
    await page.evaluate(() => { (window as unknown as { __agentPwned?: number }).__agentPwned = undefined; });
    const row = region(page, "audit").locator('tr[data-tool="activity"]').first();
    await row.getByRole("button", { name: /^Details of activity/ }).click();
    const details = region(page, "audit").locator("[data-audit-details]").first();
    await expect(details.locator("[data-audit-transport]")).toHaveText(/^(stdio|http)$/);
    const args = details.locator("[data-audit-arguments]");
    // The planted markup is drawn as characters: no element, no handler, no link.
    await expect(args).toContainText("<img src=x onerror=");
    await expect(args).toContainText("ignore-previous-instructions");
    await expect(args.locator("img, a, script")).toHaveCount(0);
    expect(await page.evaluate(() => (window as unknown as { __agentPwned?: number }).__agentPwned)).toBeUndefined();
    expect(await args.evaluate((e) => getComputedStyle(e).maxHeight)).not.toBe("none");
    // Collapsing it again.
    await row.getByRole("button", { name: /^Details of activity/ }).click();
    await expect(region(page, "audit").locator("[data-audit-details]")).toHaveCount(0);
  });
});

test.describe("#1351 failure and emptiness are separate, and per region", () => {
  test("#1351 a failed audit shows the engine's words and code, and leaves the tokens drawn", async ({ page }) => {
    await openAgents(page, "agents-audit-failed");
    await expect(region(page, "audit")).toContainText("the audit could not be read.");
    await expect(region(page, "audit").getByRole("button", { name: /try again|retry/i })).toBeVisible();
    await expect(region(page, "tokens").locator("tbody tr")).toHaveCount(5);
    await expect(card(page).locator('[data-agents-empty="confirmations"]')).toBeVisible();
  });
});
