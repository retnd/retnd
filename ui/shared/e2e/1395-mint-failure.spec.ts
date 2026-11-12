/**
 * Issue #1395 (found in review of #1394, EPIC U #1343).
 *
 * (A) A Create token that ends with no usable answer (no reply, or a reply
 * nobody could read) may have made the token. The dialog has to say so, name
 * the agent, refresh the list, point at the token if it is there, and tell
 * the user to revoke it before trying again.
 * (B) After a refusal, keyboard focus stays inside the dialog, and Create
 * token is usable again.
 *
 * There is deliberately no client timeout: it could drop a secret that is
 * still arriving, so the way out of a stuck request is a reload.
 *
 * Run through /Users/rom/workspace/xdg-pw-lock.sh only. Set E2E_VIDEO=1 for
 * the recordings.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { openAgents, openMint, tokenRow } from "./1351-agents";

const lostNotice = (dlg: ReturnType<Page["locator"]>) => dlg.locator('[data-mint-error="form"]');
const create = (dlg: ReturnType<Page["locator"]>) => dlg.getByRole("button", { name: "Create token" });

for (const scenario of ["agents-mint-lost", "agents-mint-unreadable"] as const) {
  test.describe(`#1395 (A) ${scenario}: the token was made and the answer was not`, () => {
    test(`#1395 ${scenario}: the dialog says the token may exist, names it, and says to revoke it first`, async ({ page }) => {
      await openAgents(page, scenario);
      const dlg = await openMint(page);
      await dlg.getByLabel("Name", { exact: true }).fill("lost-one");
      await create(dlg).click();
      const notice = lostNotice(dlg);
      await expect(notice).toBeVisible();
      await expect(notice).toContainText("agent:lost-one");
      await expect(notice).toContainText(/cannot be shown again/i);
      await expect(notice).toContainText(/revoke/i);
      await expect(notice).toContainText(/before (you )?try(ing)? again|then create the token again/i);
      await expect(notice).toContainText(/reload/i);
    });

    test(`#1395 ${scenario}: the list is read again and the notice points at the token that is there`, async ({ page }) => {
      await openAgents(page, scenario);
      const dlg = await openMint(page);
      await dlg.getByLabel("Name", { exact: true }).fill("lost-one");
      await create(dlg).click();
      await expect(tokenRow(page, "lost-one")).toHaveCount(1);
      await expect(lostNotice(dlg)).toContainText(/agent:lost-one is in the list/);
      // No secret anywhere: it never arrived.
      await expect(page.locator("[data-agent-token]")).toHaveCount(0);
    });

    test(`#1395 ${scenario}: the form stays open and Create token does not make a second one by itself`, async ({ page }) => {
      await openAgents(page, scenario);
      const dlg = await openMint(page);
      await dlg.getByLabel("Name", { exact: true }).fill("lost-one");
      await create(dlg).click();
      await expect(lostNotice(dlg)).toBeVisible();
      await expect(dlg.getByLabel("Name", { exact: true })).toHaveValue("lost-one");
      await expect(tokenRow(page, "lost-one")).toHaveCount(1);
    });
  });
}

test.describe("#1395 (A) agents-mint-dropped: the request was lost and nothing was made", () => {
  test("#1395 the notice still warns, and does not claim the token is in the list", async ({ page }) => {
    await openAgents(page, "agents-mint-dropped");
    const dlg = await openMint(page);
    await dlg.getByLabel("Name", { exact: true }).fill("dropped-one");
    await create(dlg).click();
    const notice = lostNotice(dlg);
    await expect(notice).toContainText("agent:dropped-one");
    await expect(notice).toContainText(/may have been created|no telling whether/i);
    await expect(notice).not.toContainText(/is in the list/);
    await expect(tokenRow(page, "dropped-one")).toHaveCount(0);
  });
});

test.describe("#1395 (B) focus stays in the dialog after a refusal", () => {
  test("#1395 a refused expiry puts focus on the expiry field, and Create token is enabled", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await openMint(page);
    await dlg.getByLabel("Name", { exact: true }).fill("too-long");
    await dlg.getByLabel("Expires in").fill("400d");
    await create(dlg).click();
    await expect(dlg.locator('[data-mint-error="expiry"]')).toBeVisible();
    await expect(dlg.getByLabel("Expires in")).toBeFocused();
    await expect(create(dlg)).toBeEnabled();
  });

  test("#1395 a refused name puts focus on the name field", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await openMint(page);
    await dlg.getByLabel("Name", { exact: true }).fill("Bad Name");
    await create(dlg).click();
    await expect(dlg.locator('[data-mint-error="name"]')).toBeVisible();
    await expect(dlg.getByLabel("Name", { exact: true })).toBeFocused();
    await expect(create(dlg)).toBeEnabled();
  });

  test("#1395 a failure that names no field puts focus on the message, inside the dialog", async ({ page }) => {
    await openAgents(page, "agents-mint-dropped");
    const dlg = await openMint(page);
    await dlg.getByLabel("Name", { exact: true }).fill("dropped-one");
    await create(dlg).click();
    await expect(lostNotice(dlg)).toBeVisible();
    await expect(lostNotice(dlg)).toBeFocused();
    await expect(create(dlg)).toBeEnabled();
  });
});
