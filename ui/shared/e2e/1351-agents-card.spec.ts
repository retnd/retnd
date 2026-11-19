/**
 * Issue #1351 (EPIC U, U2.3): the Agents card on Settings and its Tokens
 * region. Design: docs/design/1350-agents.html sections 1, 2, 5, 8, 9 and 12.
 *
 * WRITTEN, NOT RUN: no browser was launched for this change. When it is run,
 * it goes through /Users/rom/workspace/xdg-pw-lock.sh and nowhere else, one
 * run at a time.
 */
import { expect, test } from "@playwright/test";
import { SECRET, card, openAgents, region, tokenRow } from "./1351-agents";

test.describe("#1351 the card and its place", () => {
  test("#1351 sits directly after Retained-copy encryption and before Workflow, with its own anchor", async ({ page }) => {
    await openAgents(page, "default");
    const order = await page.locator("[data-settings-section]").evaluateAll((els) => els.map((e) => e.id));
    const at = (id: string) => order.indexOf(id);
    expect(at("agents")).toBeGreaterThan(-1);
    expect(at("agents")).toBe(at("encryption") + 1);
    expect(at("agents")).toBe(at("workflow") - 1);
    await page.goto("/settings?scenario=default#agents");
    await expect(card(page).getByRole("heading", { name: "Agents", level: 2 })).toBeInViewport();
  });

  test("#1351 opens with the statement, and the statement says nothing stronger than the epic proves", async ({ page }) => {
    await openAgents(page, "default");
    const statement = card(page).locator("[data-agents-statement]");
    await expect(statement).toContainText("By default it can look, and nothing else");
    await expect(statement).toContainText("waits in the confirmations list until you approve it with your password");
    await expect(statement).toContainText("not given key material, recipients, credentials or secrets");
    await expect(statement).toContainText("every call it makes, reads included");
    await expect(card(page)).not.toContainText(/\b(safe|secure|protected|trusted|verified)\b/i);
  });

  test("#1351 the default deployment reads as normal: no token, nothing waiting, nothing audited", async ({ page }) => {
    await openAgents(page, "default");
    await expect(card(page).locator('[data-agents-empty="tokens"]')).toHaveText(
      "No agent has a token. Nothing can connect to this deployment as an agent."
    );
    await expect(region(page, "tokens").getByRole("button", { name: "Create token" })).toBeVisible();
    await expect(card(page).locator('[data-agents-empty="confirmations"]')).toHaveText("Nothing is waiting for you.");
    await expect(card(page).locator('[data-agents-empty="audit"]')).toHaveText("No agent has called anything yet.");
    await expect(card(page).locator("[data-agents-waiting]")).toHaveCount(0);
  });

  test("#1351 draws four regions in the order a person uses them", async ({ page }) => {
    await openAgents(page, "agents-pending");
    const regions = await card(page).locator("[data-agents-region]").evaluateAll((els) => els.map((e) => e.getAttribute("data-agents-region")));
    expect(regions).toEqual(["tokens", "confirmations", "audit"]);
    // With something waiting the regions do not reorder: the heading counts and links.
    const waiting = card(page).locator("[data-agents-waiting]");
    await expect(waiting).toHaveText("2 waiting for you");
    await expect(waiting).toHaveAttribute("href", "#agents-confirmations");
  });
});

test.describe("#1351 the tokens table", () => {
  test("#1351 has the nine columns, one row per token, newest data as the engine gave it", async ({ page }) => {
    await openAgents(page);
    const heads = await region(page, "tokens").locator("thead th").allTextContents();
    expect(heads.map((h) => h.trim())).toEqual(["Name", "Prefix", "Scope", "Owner", "Created", "Expires", "Last used", "Status", "Actions"]);
    await expect(region(page, "tokens").locator("tbody tr")).toHaveCount(5);
    const nightly = tokenRow(page, "nightly-triage");
    await expect(nightly).toContainText("agent:nightly-triage");
    await expect(nightly.locator("[data-token-prefix]")).toHaveText("rat_q7Hn");
    await expect(nightly).toContainText("admin");
    await expect(nightly).toContainText("in 76 days");
    await expect(tokenRow(page, "settings-helper")).toContainText("never");
    await expect(tokenRow(page, "old-weekly-report")).toContainText("43 days ago");
  });

  test("#1351 a prefix is eight characters and the row carries no more of a token than that", async ({ page }) => {
    await openAgents(page);
    for (const prefix of await region(page, "tokens").locator("[data-token-prefix]").allInnerTexts()) {
      expect(prefix.length).toBe(8);
    }
    expect(await card(page).innerHTML()).not.toContain(SECRET.slice(-12));
  });

  test("#1351 each status is a glyph, a word and an edge, in the body ink", async ({ page }) => {
    await openAgents(page);
    const chip = (name: string) => tokenRow(page, name).locator('[data-chip="status"]');
    await expect(chip("nightly-triage")).toHaveText("active");
    await expect(chip("nightly-triage").locator(".agchip__glyph svg")).toHaveCount(1);
    await expect(chip("old-weekly-report")).toHaveText("◷expired");
    await expect(chip("laptop-experiment")).toHaveText("revoked");
    await expect(chip("laptop-experiment").locator(".agchip__glyph svg")).toHaveCount(1);
    const style = (name: string) => chip(name).evaluate((e) => {
      const c = getComputedStyle(e);
      return { border: c.borderTopStyle, color: c.color };
    });
    expect((await style("old-weekly-report")).border).toBe("dashed");
    expect((await style("nightly-triage")).border).toBe("solid");
    // Expired and revoked rows are not dimmed: the same ink as an active one.
    expect((await style("old-weekly-report")).color).toBe((await style("nightly-triage")).color);
    expect(await tokenRow(page, "old-weekly-report").evaluate((e) => getComputedStyle(e).opacity)).toBe("1");
  });

  test("#1351 offers Revoke on active rows only, and no edit, extend or rotate anywhere", async ({ page }) => {
    await openAgents(page);
    await expect(tokenRow(page, "nightly-triage").getByRole("button", { name: "Revoke agent:nightly-triage" })).toBeVisible();
    await expect(tokenRow(page, "old-weekly-report").getByRole("button")).toHaveCount(0);
    await expect(tokenRow(page, "laptop-experiment").getByRole("button")).toHaveCount(0);
    await expect(region(page, "tokens").getByRole("button", { name: /edit|extend|rename|rotate|renew/i })).toHaveCount(0);
  });

  test("#1351 Revoke asks first with the consequence in the button, then the token is revoked", async ({ page }) => {
    await openAgents(page);
    await tokenRow(page, "nightly-triage").getByRole("button", { name: "Revoke agent:nightly-triage" }).click();
    const dlg = page.getByRole("dialog", { name: "Revoke agent:nightly-triage?" });
    await expect(dlg).toContainText("Its next request is refused");
    await expect(dlg).toContainText("cannot be brought back, only replaced");
    // Cancel changes nothing.
    await dlg.getByRole("button", { name: "Cancel" }).click();
    await expect(tokenRow(page, "nightly-triage")).toHaveAttribute("data-status", "active");
    await tokenRow(page, "nightly-triage").getByRole("button", { name: "Revoke agent:nightly-triage" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Revoke agent:nightly-triage" }).click();
    await expect(tokenRow(page, "nightly-triage")).toHaveAttribute("data-status", "revoked");
    await expect(tokenRow(page, "nightly-triage").getByRole("button")).toHaveCount(0);
    await expect(tokenRow(page, "nightly-triage").locator('[data-chip="status"]')).toHaveText("revoked");
  });

  test("#1351 the revoke dialog draws the revoke twin the browser composes", async ({ page }) => {
    await openAgents(page);
    await tokenRow(page, "nightly-triage").getByRole("button", { name: "Revoke agent:nightly-triage" }).click();
    // Q13: the revoke twin is composed in the browser and pinned to cliecho.
    await expect(page.getByRole("dialog")).toContainText("retnd agent token revoke tok_01");
  });
});

test.describe("#1351 what each scope can call, and the transport line", () => {
  test("#1351 the disclosure is closed, and lists what the catalogue lists, by scope", async ({ page }) => {
    await openAgents(page);
    const disclosure = card(page).locator("[data-agents-scopes]");
    await expect(disclosure).not.toHaveAttribute("open", "");
    await disclosure.locator("summary").click();
    await expect(disclosure.locator('[data-scope-tools="read"] li')).toHaveCount(19);
    await expect(disclosure.locator('[data-scope-tools="operate"] li')).toHaveCount(7);
    await expect(disclosure.locator('[data-scope-tools="configure"] li')).toHaveCount(4);
    await expect(disclosure.locator('[data-scope-tools="operate"]')).toContainText("run_cycle");
    await expect(disclosure.locator('[data-scope-tools="read"]')).not.toContainText("run_cycle");
    // Plain text: no toggle per tool, and no sentence about inclusion.
    await expect(disclosure.getByRole("button")).toHaveCount(0);
    await expect(disclosure.getByRole("checkbox")).toHaveCount(0);
    await expect(disclosure).not.toContainText(/also|includes|can also call/i);
  });

  test("#1351 a catalogue that fails is the engine's error in the disclosure, and the tokens still draw", async ({ page }) => {
    await openAgents(page, "agents-catalogue-failed");
    await card(page).locator("[data-agents-scopes] > summary").click();
    await expect(card(page).locator("[data-agents-scopes]")).toContainText("the catalogue could not be read.");
    await expect(region(page, "tokens").locator("tbody tr")).toHaveCount(5);
  });

  test("#1351 states how agents connect and where the HTTP transport is set, as text with no control", async ({ page }) => {
    await openAgents(page);
    const line = card(page).locator("[data-agents-transport]");
    await expect(line).toContainText("HTTP transport: off");
    await expect(line).toContainText("retnd agent serve --stdio");
    await expect(line).toContainText("http_transport: on");
    await expect(line).toContainText("cannot be read by an older build");
    await expect(line.getByRole("button")).toHaveCount(0);
    await expect(card(page).getByRole("switch")).toHaveCount(0);
    await expect(card(page).getByRole("checkbox")).toHaveCount(0);
  });
});

test.describe("#1367 the transport line follows the capabilities", () => {
  test("#1367 says HTTP transport: on when the engine reports it on", async ({ page }) => {
    await openAgents(page, "agents-http-on");
    await expect(card(page).locator("[data-agents-transport]")).toContainText("HTTP transport: on");
  });
});

test.describe("#1351 a read-only page", () => {
  test("#1351 keeps the lists readable and removes every mutating control", async ({ page }) => {
    await openAgents(page, "agents-read-only");
    await expect(region(page, "tokens").locator("tbody tr")).toHaveCount(5);
    await expect(card(page).getByRole("button", { name: /Create token|Revoke|Approve|Deny/ })).toHaveCount(0);
  });
});

