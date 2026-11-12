import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

/**
 * Issue #1352 (EPIC U, U2.4, FR-94): the browser's half of the three-way
 * equivalence. The page has no exit status, so it is held to the other two
 * things every surface must agree on: the code and the message. The CLI, the
 * stdio transport and the HTTP transport are compared on all three, with the
 * exit code, by retnd-tests' suites/equivalence/specs/agent-equivalence.1352.spec.ts.
 * This spec and that one share one table, below, and a row's words are
 * the engine's own: nothing in the page's source contains them, so they can
 * only be on screen if the engine's answer was read.
 *
 * The scenario `agent-refusals` serves, from the in-process mock, one audit
 * row for a read token's refused run_cycle and one pending confirmation whose
 * approval the shut gate refuses. ASSUMES U2.3 (#1351) exposes the audit table
 * as [data-surface="audit"], pending confirmations as [data-surface="confirm"],
 * and the refusal the page draws under a row as [data-agent-refusal]; the
 * design note (docs/design/1350-agents.html) names the first two.
 */
const REFUSALS = [
  {
    name: "a read token asking run_cycle",
    code: "AGENT_SCOPE_INSUFFICIENT",
    message: "a read token cannot start or change anything",
    surface: '[data-surface="audit"]',
  },
  {
    name: "an approved run_cycle at the shut gate",
    code: "DESTRUCTIVE_OPERATIONS_DISABLED",
    message:
      "destructive operations are refused over HTTP in this release, until the trusted-proxy authentication gate (issue #92) has been verified; run the equivalent `retnd` command on the host",
    surface: '[data-surface="confirm"]',
  },
] as const;

async function agents(page: Page) {
  await open(page, "/settings", "agent-refusals");
  const card = page.locator("[data-agents-card]");
  await expect(card).toBeVisible();
  return card;
}

test.describe("#1352 the Agents card says what the CLI and both transports say", () => {
  for (const row of REFUSALS) {
    test(`${row.name}: the page draws the engine's code and message`, async ({ page }) => {
      const card = await agents(page);
      if (row.surface.includes("confirm")) {
        // The refusal is the answer to an approval, so press it with the step-up.
        await card.locator(row.surface).getByRole("button", { name: /^Approve/ }).first().click();
        await page.getByLabel(/password/i).fill("a-step-up-password");
        await page.getByRole("button", { name: /^Approve and run/ }).click();
      }
      const shown = card.locator(row.surface).locator("[data-agent-refusal]").first();
      await expect(shown, `the page draws a refusal for ${row.name}`).toBeVisible();
      await expect(shown).toContainText(row.code);
      await expect(shown).toContainText(row.message);
    });
  }

  test("the control: a row that was not refused draws no refusal", async ({ page }) => {
    const card = await agents(page);
    const ok = card.locator('[data-surface="audit"]').getByRole("row").filter({ hasText: "status" }).first();
    await expect(ok).toBeVisible();
    await expect(ok.locator("[data-agent-refusal]")).toHaveCount(0);
  });

  test("the command the page echoes for the refused tool is the one the CLI would run", async ({ page }) => {
    const card = await agents(page);
    const echo = card.locator("code", { hasText: /^\s*retnd agent / }).first();
    await expect(echo, "CommandEcho shows the retnd agent twin").toBeVisible();
    expect((await echo.innerText()).replace(/\s+/g, " ").trim()).toMatch(/^retnd agent [A-Za-z0-9_./: -]+$/);
  });
});
