/**
 * Issue #1351 (EPIC U, U2.3): pending confirmations. Design:
 * docs/design/1350-agents.html section 6.
 *
 * What these drive: Approve opens the step-up and calls nothing; Approve and
 * run replays the recorded request; Deny is one click and no password; the
 * engine is the judge of expiry; the shut operation gate is a strip in the
 * gate's own words with code, exit code and correlation id; and no command is
 * composed in the browser (the only command a row draws is the record's own).
 *
 * "Calls nothing" is asserted through what the engine would have changed: the
 * mock is in-process, so there is no request to watch, and a request that ran
 * would leave the row and add an audit row.
 *
 * WRITTEN, NOT RUN: no browser was launched for this change. When it is run,
 * it goes through /Users/rom/workspace/xdg-pw-lock.sh and nowhere else, one
 * run at a time.
 */
import { expect, test } from "@playwright/test";
import { PASSWORD, card, openAgents, pending, region } from "./1351-agents";

const GATE =
  "destructive operations are refused over HTTP in this release, until the trusted-proxy authentication gate (issue #92) has been verified; run the equivalent retnd command on the host";

test.describe("#1351 the pending rows", () => {
  test("#1351 say who asked, what, for which set, the recorded command, and when it expires", async ({ page }) => {
    await openAgents(page, "agents-pending");
    const row = pending(page, "cf_02");
    await expect(row).toContainText("Starts a recovery test of one backup.");
    await expect(row.locator("[data-confirmation-token]")).toHaveText("agent:recovery-drill");
    await expect(row).toContainText("production/postgres-primary");
    await expect(row.locator("[data-confirmation-command] code")).toHaveText("retnd assurance recovery-test production/postgres-primary");
    await expect(row.locator("[data-confirmation-expires]")).toContainText(/expires \d\d:\d\d \(in 0:\d\d\)/);
    // No request body, no JSON, and no cap or TTL control.
    expect(await row.innerText()).not.toMatch(/[{}]|"tool"|max_pending|ttl/i);
    await expect(row.getByRole("button")).toHaveCount(2);
  });

  test("#1351 the row under a minute takes the warning edge and the clock glyph, and Approve is still on", async ({ page }) => {
    await openAgents(page, "agents-pending");
    const urgent = pending(page, "cf_02");
    await expect(urgent).toHaveAttribute("data-urgent", "");
    await expect(urgent.locator("[data-confirmation-expires]")).toContainText("◷");
    expect(await urgent.evaluate((e) => getComputedStyle(e).borderTopWidth)).toBe("2px");
    await expect(urgent.getByRole("button", { name: "Approve", exact: true })).toBeEnabled();
    await expect(pending(page, "cf_01")).not.toHaveAttribute("data-urgent", "");
  });

  test("#1351 Approve and Deny are the same size and neutral, so the row never recommends running it", async ({ page }) => {
    await openAgents(page, "agents-pending");
    const row = pending(page, "cf_01");
    const a = await row.getByRole("button", { name: "Approve", exact: true }).boundingBox();
    const d = await row.getByRole("button", { name: "Deny", exact: true }).boundingBox();
    expect(a!.width).toBeCloseTo(d!.width, 0);
    expect(a!.height).toBeCloseTo(d!.height, 0);
    const cls = (name: string) => row.getByRole("button", { name, exact: true }).getAttribute("class");
    expect(await cls("Approve")).not.toContain("btn--primary");
    expect(await cls("Deny")).not.toContain("btn--destructive");
  });

  test("#1351 the order is not changed by something waiting: the tokens region is still first", async ({ page }) => {
    await openAgents(page, "agents-pending");
    const first = await card(page).locator("[data-agents-region]").first().getAttribute("data-agents-region");
    expect(first).toBe("tokens");
  });
});

test.describe("#1351 Approve is a step-up, and runs the recorded request", () => {
  test("#1351 pressing Approve asks for the password and runs nothing", async ({ page }) => {
    await openAgents(page, "agents-pending");
    const auditBefore = await region(page, "audit").locator("tbody tr").count();
    const row = pending(page, "cf_01");
    await row.getByRole("button", { name: "Approve", exact: true }).click();
    await expect(row.getByLabel("Your password")).toBeVisible();
    await expect(row).toContainText("sent once with this request and not kept");
    await expect(row.getByRole("button", { name: "Approve and run" })).toBeDisabled();
    await page.waitForTimeout(500);
    await expect(row).toBeVisible();
    await expect(pending(page, "cf_02")).toBeVisible();
    await expect(region(page, "audit").locator("tbody tr")).toHaveCount(auditBefore);
    // Cancel collapses it and the row is as it was.
    await row.getByRole("button", { name: "Cancel" }).click();
    await expect(row.getByLabel("Your password")).toHaveCount(0);
    await expect(row.getByRole("button", { name: "Approve", exact: true })).toBeVisible();
  });

  test("#1351 a correct password runs it: the row leaves the list and the call is in the audit", async ({ page }) => {
    await openAgents(page, "agents-pending");
    const row = pending(page, "cf_02");
    await row.getByRole("button", { name: "Approve", exact: true }).click();
    await row.getByLabel("Your password").fill(PASSWORD);
    await row.getByRole("button", { name: "Approve and run" }).click();
    await expect(row).toHaveCount(0);
    await expect(card(page).locator("[data-agents-waiting]")).toHaveText("1 waiting for you");
    await expect(region(page, "audit").locator('tr[data-tool="recovery_test_start"]').first()).toContainText("202");
    await expect(card(page).locator("[data-agents-notice]")).toHaveText("Approved. The request ran.");
  });

  test("#1351 a wrong password runs nothing, says so, keeps the row, and forgets what was typed", async ({ page }) => {
    await openAgents(page, "agents-pending");
    const row = pending(page, "cf_01");
    await row.getByRole("button", { name: "Approve", exact: true }).click();
    await row.getByLabel("Your password").fill("not-the-password");
    await row.getByRole("button", { name: "Approve and run" }).click();
    const err = row.locator("[data-approve-error]");
    await expect(err).toContainText("That password was not accepted");
    await expect(err).toContainText("Nothing was run");
    await expect(row.getByLabel("Your password")).toHaveValue("");
    await expect(row.getByRole("button", { name: "Approve and run" })).toBeDisabled();
    await expect(row).toBeVisible();
  });

  test("#1351 on an unencrypted connection the question comes before the password is sent", async ({ page }) => {
    await openAgents(page, "agents-insecure");
    const row = pending(page, "cf_01");
    await row.getByRole("button", { name: "Approve", exact: true }).click();
    await row.getByLabel("Your password").fill(PASSWORD);
    await row.getByRole("button", { name: "Approve and run" }).click();
    const ask = page.getByRole("dialog", { name: "This connection is not encrypted" });
    await expect(ask).toBeVisible();
    await ask.getByRole("button", { name: "Cancel" }).click();
    await expect(row.locator("[data-approve-error]")).toContainText("Nothing was sent");
    await expect(row).toBeVisible();
    await row.getByRole("button", { name: "Approve and run" }).click();
    await page.getByRole("dialog", { name: "This connection is not encrypted" }).getByRole("button", { name: "Continue anyway" }).click();
    await expect(row).toHaveCount(0);
  });

  test("#1351 the engine's expiry refusal is shown as written, and the button never locked itself", async ({ page }) => {
    await openAgents(page, "agents-pending-expired");
    const row = pending(page, "cf_02");
    await row.getByRole("button", { name: "Approve", exact: true }).click();
    await row.getByLabel("Your password").fill(PASSWORD);
    await expect(row.getByRole("button", { name: "Approve and run" })).toBeEnabled();
    await row.getByRole("button", { name: "Approve and run" }).click();
    const err = row.locator("[data-approve-error]");
    await expect(err).toContainText("this confirmation expired and nothing was run");
    await expect(err).toContainText("AGENT_CONFIRMATION_EXPIRED");
    await expect(err).toContainText("cid_mockexpired");
  });

  test("#1351 the shut gate is a strip in the gate's own words, with code, exit 50 and correlation id, and no control", async ({ page }) => {
    await openAgents(page, "agents-pending-gate");
    const row = pending(page, "cf_01");
    await row.getByRole("button", { name: "Approve", exact: true }).click();
    await row.getByLabel("Your password").fill(PASSWORD);
    await row.getByRole("button", { name: "Approve and run" }).click();
    const strip = card(page).locator("[data-result-strip]");
    await expect(strip).toContainText(GATE);
    await expect(strip.locator("[data-strip-code]")).toHaveText("DESTRUCTIVE_OPERATIONS_DISABLED");
    await expect(strip.locator("[data-strip-exit]")).toHaveText("exit 50");
    await expect(strip.locator("[data-strip-cid]")).toHaveText("cid_mockgate01");
    await expect(strip.getByRole("button")).toHaveCount(0);
    expect(await strip.evaluate((e) => getComputedStyle(e).borderTopWidth)).toBe("2px");
    // The record left the list; the strip stays, and the audit has the refusal.
    await expect(pending(page, "cf_01")).toHaveCount(0);
    await expect(region(page, "audit").locator('tr[data-tool="run_cycle"]').first()).toContainText("DESTRUCTIVE_OPERATIONS_DISABLED");
    await expect(card(page).locator("[data-agents-notice]")).toContainText("the engine refused it");
  });
});

test.describe("#1351 Deny", () => {
  test("#1351 is one click and no password, runs nothing, and ends the request", async ({ page }) => {
    await openAgents(page, "agents-pending");
    const auditBefore = await region(page, "audit").locator("tbody tr").count();
    const row = pending(page, "cf_01");
    await row.getByRole("button", { name: "Deny", exact: true }).click();
    await expect(row).toHaveCount(0);
    await expect(page.getByLabel("Your password")).toHaveCount(0);
    await expect(region(page, "audit").locator("tbody tr")).toHaveCount(auditBefore);
    await expect(card(page).locator("[data-agents-notice]")).toHaveText("Denied. Nothing ran.");
    await expect(card(page).locator("[data-agents-waiting]")).toHaveText("1 waiting for you");
  });

  test("#1351 denying the last one leaves the plain empty sentence", async ({ page }) => {
    await openAgents(page, "agents-pending");
    for (const id of ["cf_01", "cf_02"]) await pending(page, id).getByRole("button", { name: "Deny", exact: true }).click();
    await expect(card(page).locator('[data-agents-empty="confirmations"]')).toHaveText("Nothing is waiting for you.");
    await expect(card(page).locator("[data-agents-waiting]")).toHaveCount(0);
  });
});

test.describe("#1351 the echo is never composed in the browser", () => {
  test("#1351 the only command a row draws is the one the record carries, byte for byte", async ({ page }) => {
    await openAgents(page, "agents-pending");
    await expect(pending(page, "cf_01").locator("[data-confirmation-command] code")).toHaveText("retnd run");
    await expect(pending(page, "cf_02").locator("[data-confirmation-command] code")).toHaveText(
      "retnd assurance recovery-test production/postgres-primary"
    );
    // Approve and deny have no twin in the response, so none is invented for them.
    await expect(pending(page, "cf_01")).not.toContainText("confirmations approve");
    await expect(pending(page, "cf_01")).not.toContainText("confirmations deny");
    await expect(pending(page, "cf_01").locator("code")).toHaveCount(1);
  });

  test("#1351 the sentence is the catalogue's, and a row whose tool the catalogue lacks names the tool instead", async ({ page }) => {
    await openAgents(page, "agents-pending");
    await expect(pending(page, "cf_01").locator(".ag-row__sentence")).toHaveText("Runs a backup cycle for every enabled backup set.");
    await openAgents(page, "agents-catalogue-failed");
    await expect(pending(page, "cf_01").locator(".ag-row__sentence")).toHaveText("run_cycle");
  });

  test("#1351 a record that was already decided is not listed as waiting", async ({ page }) => {
    await openAgents(page, "agents-pending");
    await expect(pending(page, "cf_00")).toHaveCount(0);
    await expect(card(page).locator("[data-agents-waiting]")).toHaveText("2 waiting for you");
  });
});

test.describe("#1351 a read-only page", () => {
  test("#1351 still shows what is waiting, and says why it cannot be answered", async ({ page }) => {
    await openAgents(page, "agents-read-only");
    await expect(pending(page, "cf_01")).toContainText("Runs a backup cycle for every enabled backup set.");
    await expect(pending(page, "cf_01").locator("[data-confirmation-command] code")).toHaveText("retnd run");
    await expect(pending(page, "cf_01").locator("[data-readonly-note]")).toContainText("needs a page that can be written to");
    await expect(card(page).getByRole("button", { name: /Approve|Deny/ })).toHaveCount(0);
  });
});
