/**
 * Issues #1392 and #1393 (found in the Agents card from #1367, EPIC U #1343).
 *
 * #1392: Cancel, Escape or the backdrop while a token is being created lost
 * the secret. The create must not be cancellable once it is in flight, and
 * the reveal opens when it completes.
 * #1393: closing "Allow more than reading" left operate or configure
 * selected. Closed means read (design 1350 section 3).
 *
 * Run through /Users/rom/workspace/xdg-pw-lock.sh only. Set E2E_VIDEO=1 for
 * the recordings.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { SECRET, card, openAgents, openMint, tokenRow } from "./1351-agents";

const reveal = (page: Page) => page.getByRole("dialog", { name: /^Your token for agent:/ });
const beat = (p: Page, ms = 700) => p.waitForTimeout(ms);

test.describe("#1392 a create in flight cannot be cancelled", () => {
  for (const how of ["Escape", "Cancel", "backdrop"] as const) {
    test(`#1392 ${how} while the token is being created still ends in the reveal`, async ({ page }) => {
      await openAgents(page, "default");
      const dlg = await openMint(page);
      await dlg.getByLabel("Name", { exact: true }).fill("in-flight");
      await beat(page);
      await dlg.getByRole("button", { name: "Create token" }).click();
      if (how === "Escape") await page.keyboard.press("Escape");
      else if (how === "Cancel") await dlg.getByRole("button", { name: "Cancel" }).click();
      else await page.mouse.click(5, 5);
      // The reveal opens with the secret, and it is the only way out.
      await expect(reveal(page)).toBeVisible();
      await expect(reveal(page).getByLabel("Your token")).toHaveValue(SECRET);
      await beat(page);
      await reveal(page).getByRole("button", { name: "I have stored it" }).click();
      await expect(tokenRow(page, "in-flight")).toHaveCount(1);
    });
  }

  test("#1392 before Create is pressed, Escape and Cancel still close the form and make nothing", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await openMint(page);
    await dlg.getByLabel("Name", { exact: true }).fill("never");
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(card(page).locator('[data-agents-empty="tokens"]')).toBeVisible();
  });
});

test.describe("#1393 closing the fold means read", () => {
  test("#1393 operate chosen, fold closed: the token is read, and the echo says read", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await openMint(page);
    const fold = dlg.getByRole("button", { name: "Allow more than reading" });
    await fold.click();
    await beat(page);
    await dlg.getByRole("radio", { name: /^operate/ }).check();
    await beat(page);
    await fold.click();
    await expect(dlg.getByRole("radio", { name: /^read/ })).toBeChecked();
    await dlg.getByLabel("Name", { exact: true }).fill("closed-fold");
    await expect(dlg).toContainText("--scope read");
    await expect(dlg).not.toContainText("--scope operate");
    await beat(page);
    await dlg.getByRole("button", { name: "Create token" }).click();
    await reveal(page).getByRole("button", { name: "I have stored it" }).click();
    await expect(tokenRow(page, "closed-fold").locator('[data-chip="scope"]')).toHaveText("read");
  });

  test("#1393 configure chosen, fold closed and opened again: read is still selected", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await openMint(page);
    const fold = dlg.getByRole("button", { name: "Allow more than reading" });
    await fold.click();
    await dlg.getByRole("radio", { name: /^configure/ }).check();
    await fold.click();
    await fold.click();
    await expect(dlg.getByRole("radio", { name: /^configure/ })).not.toBeChecked();
    await expect(dlg.getByRole("radio", { name: /^read/ })).toBeChecked();
  });

  test("#1393 a wider scope chosen with the fold open is still minted", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await openMint(page);
    await dlg.getByRole("button", { name: "Allow more than reading" }).click();
    await dlg.getByRole("radio", { name: /^configure/ }).check();
    await dlg.getByLabel("Name", { exact: true }).fill("wide");
    await dlg.getByRole("button", { name: "Create token" }).click();
    await reveal(page).getByRole("button", { name: "I have stored it" }).click();
    await expect(tokenRow(page, "wide").locator('[data-chip="scope"]')).toContainText("configure");
  });
});
