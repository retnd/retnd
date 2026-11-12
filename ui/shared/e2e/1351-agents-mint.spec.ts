/**
 * Issue #1351 (EPIC U, U2.3): Create token and the one time the token is
 * shown. Design: docs/design/1350-agents.html sections 3 and 4.
 *
 * The falsification the design names: the token string is in the DOM for the
 * life of the one dialog and in no list row, tooltip, toast, live region,
 * title attribute, error message or browser storage, before or after.
 *
 * WRITTEN, NOT RUN: no browser was launched for this change. When it is run,
 * it goes through /Users/rom/workspace/xdg-pw-lock.sh and nowhere else, one
 * run at a time.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { SECRET, card, openAgents, openMint, region, tokenRow } from "./1351-agents";

const reveal = (page: Page) => page.getByRole("dialog", { name: /^Your token for agent:/ });

async function mintRead(page: Page, name = "weekly-digest") {
  const dlg = await openMint(page);
  await dlg.getByLabel("Name", { exact: true }).fill(name);
  await dlg.getByRole("button", { name: "Create token" }).click();
  await expect(reveal(page)).toBeVisible();
  return reveal(page);
}

/** Everywhere the token must not be once the dialog is gone. */
async function expectNowhere(page: Page) {
  const html = await page.content();
  expect(html, "the token is in the page").not.toContain(SECRET);
  expect(html, "the token's tail is in the page").not.toContain(SECRET.slice(8));
  const stored = await page.evaluate(() => JSON.stringify([{ ...localStorage }, { ...sessionStorage }, document.title]));
  expect(stored).not.toContain(SECRET.slice(8));
}

test.describe("#1351 the mint form", () => {
  test("#1351 has three fields, and read is the scope it opens on", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await openMint(page);
    await expect(dlg.getByLabel("Name", { exact: true })).toBeVisible();
    await expect(dlg.getByLabel("Expires in", { exact: true })).toHaveValue("90d");
    await expect(dlg.getByRole("radio", { name: /^read/ })).toBeChecked();
    await expect(dlg.getByRole("radio")).toHaveCount(1);
    // The fold is closed by default and holds the other two, with the epic's words.
    const fold = dlg.getByRole("button", { name: "Allow more than reading" });
    await expect(fold).toHaveAttribute("aria-expanded", "false");
    await fold.click();
    await expect(dlg.getByRole("radio")).toHaveCount(3);
    await expect(dlg.getByRole("radio", { name: /^operate/ })).not.toBeChecked();
    await expect(dlg.getByRole("radio", { name: /^configure/ })).not.toBeChecked();
    await expect(dlg).toContainText("Nothing runs until you approve each request here.");
    await expect(dlg).toContainText("Nothing changes until you approve each request here.");
    await expect(dlg).not.toContainText(/recommend|safe/i);
  });

  test("#1351 opening the fold changes nothing: a token made with it closed is a read token", async ({ page }) => {
    await openAgents(page, "default");
    let dlg = await openMint(page);
    await dlg.getByRole("button", { name: "Allow more than reading" }).click();
    await dlg.getByRole("button", { name: "Allow more than reading" }).click();
    await dlg.getByLabel("Name", { exact: true }).fill("fold-test");
    await dlg.getByRole("button", { name: "Create token" }).click();
    dlg = reveal(page);
    await dlg.getByRole("button", { name: "I have stored it" }).click();
    await expect(tokenRow(page, "fold-test").locator('[data-chip="scope"]')).toHaveText("read");
  });

  test("#1351 Create token stays off until there is a name, and Cancel makes nothing", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await openMint(page);
    await expect(dlg.getByRole("button", { name: "Create token" })).toBeDisabled();
    await dlg.getByLabel("Name", { exact: true }).fill("nothing");
    await dlg.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(card(page).locator('[data-agents-empty="tokens"]')).toBeVisible();
  });

  test("#1351 an expiry the engine refuses is shown as the engine wrote it, beside its field, and keeps what was typed", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await openMint(page);
    await dlg.getByLabel("Name", { exact: true }).fill("too-long");
    await dlg.getByLabel("Expires in", { exact: true }).fill("400d");
    await dlg.getByRole("button", { name: "Create token" }).click();
    const err = dlg.locator('[data-mint-error="expiry"]');
    await expect(err).toHaveText("expires_in: may not exceed 365d.");
    await expect(dlg.getByLabel("Name", { exact: true })).toHaveValue("too-long");
    await expect(dlg.getByLabel("Expires in", { exact: true })).toHaveValue("400d");
    await expect(dlg.getByLabel("Expires in", { exact: true })).toHaveAttribute("aria-invalid", "true");
    // The edge is two pixels, so the refusal is more than a colour.
    expect(await dlg.getByLabel("Expires in", { exact: true }).evaluate((e) => getComputedStyle(e).borderTopWidth)).toBe("2px");
    await expect(page.getByRole("dialog", { name: /^Your token for/ })).toHaveCount(0);
    await expect(card(page).locator('[data-agents-empty="tokens"]')).toBeVisible();
  });

  test("#1351 a name the engine refuses is shown beside the name", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await openMint(page);
    await dlg.getByLabel("Name", { exact: true }).fill("Bad Name");
    await dlg.getByRole("button", { name: "Create token" }).click();
    await expect(dlg.locator('[data-mint-error="name"]')).toContainText("name: must be 1 to 40 lowercase letters");
  });

  test("#1351 on an unencrypted connection the question comes before the token is requested", async ({ page }) => {
    await openAgents(page, "agents-insecure");
    const dlg = await openMint(page);
    await dlg.getByLabel("Name", { exact: true }).fill("over-http");
    await dlg.getByRole("button", { name: "Create token" }).click();
    const ask = page.getByRole("dialog", { name: "This connection is not encrypted" });
    await expect(ask).toBeVisible();
    await ask.getByRole("button", { name: "Cancel" }).click();
    await expect(dlg.locator('[data-mint-error="form"]')).toContainText("Nothing was created");
    await expect(page.getByRole("dialog", { name: /^Your token for/ })).toHaveCount(0);
    await dlg.getByRole("button", { name: "Create token" }).click();
    await page.getByRole("dialog", { name: "This connection is not encrypted" }).getByRole("button", { name: "Continue anyway" }).click();
    await expect(reveal(page)).toBeVisible();
  });
});

test.describe("#1351 the token is shown once", () => {
  test("#1351 the token is in a read-only field that takes focus, and in the dialog's own words", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await mintRead(page, "nightly-triage");
    const field = dlg.getByLabel("Your token");
    await expect(field).toHaveValue(SECRET);
    await expect(field).toHaveAttribute("readonly", "");
    await expect(field).toBeFocused();
    await expect(dlg).toContainText("This is the only time this token is shown");
    await expect(dlg).toContainText("revoke the token and make another");
    await expect(dlg).toContainText("retnd agent serve --stdio --token-file PATH");
    await expect(dlg).toContainText("RETND_AGENT_TOKEN");
    // Q13: the browser composes the create twin (pinned to cliecho by agentCommands.1367.test.ts), so the reveal keeps the line the form showed.
    await expect(dlg).toContainText("retnd agent token create nightly-triage --scope read --expires-in 90d");
    // The field is a textarea, so its text is its value: the token is in the field and in no other text of the dialog.
    expect(await dlg.evaluate((d) => { const c = d.cloneNode(true) as HTMLElement; c.querySelectorAll("textarea").forEach((t) => t.remove()); return c.textContent ?? ""; })).not.toContain(SECRET);
  });

  test("#1351 has exactly one way out: no close glyph, no Cancel, Escape and the backdrop do nothing", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await mintRead(page);
    await expect(dlg.getByRole("button")).toHaveCount(2);
    await expect(dlg.getByRole("button", { name: "Copy" })).toBeVisible();
    await expect(dlg.getByRole("button", { name: "I have stored it" })).toBeVisible();
    await expect(dlg.getByRole("button", { name: /cancel|close|dismiss|×|x$/i })).toHaveCount(0);
    await page.keyboard.press("Escape");
    await expect(dlg).toBeVisible();
    await page.mouse.click(5, 5);
    await expect(dlg).toBeVisible();
    await expect(dlg.getByLabel("Your token")).toHaveValue(SECRET);
  });

  test("#1351 keeps focus inside the dialog", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await mintRead(page);
    for (let i = 0; i < 6; i++) {
      await page.keyboard.press("Tab");
      expect(await dlg.evaluate((d) => d.contains(document.activeElement))).toBe(true);
    }
    for (let i = 0; i < 6; i++) {
      await page.keyboard.press("Shift+Tab");
      expect(await dlg.evaluate((d) => d.contains(document.activeElement))).toBe(true);
    }
  });

  test("#1351 Copy announces Copied in a live region that does not hold the token", async ({ page, context }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await openAgents(page, "default");
    const dlg = await mintRead(page);
    await dlg.getByRole("button", { name: "Copy" }).click();
    const live = dlg.locator('[role="status"]');
    await expect(live).toHaveText("Copied");
    expect(await live.innerHTML()).not.toContain(SECRET.slice(8));
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(SECRET);
  });

  test("#1351 wraps the token rather than scrolling it, so all of it is on screen", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 700 });
    await openAgents(page, "default");
    const field = (await mintRead(page)).getByLabel("Your token");
    expect(await field.evaluate((e) => e.scrollWidth <= e.clientWidth + 1)).toBe(true);
    expect(await field.evaluate((e) => e.scrollHeight <= e.clientHeight + 1)).toBe(true);
  });

  test("#1351 after I have stored it the token is nowhere: no row, tooltip, toast, live region, attribute or storage", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await mintRead(page, "weekly-digest");
    await dlg.getByRole("button", { name: "I have stored it" }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const row = tokenRow(page, "weekly-digest");
    await expect(row.locator("[data-token-prefix]")).toHaveText("rat_EXAM");
    await expect(row).toContainText("in 90 days");
    await expectNowhere(page);
    // Hovering the new row's controls opens tooltips; none of them holds it either.
    await row.locator('[data-chip="scope"]').hover();
    await row.locator("[data-token-prefix]").hover();
    await expectNowhere(page);
  });

  test("#1351 a second dialog does not still hold the first token", async ({ page }) => {
    await openAgents(page, "default");
    await (await mintRead(page, "first-one")).getByRole("button", { name: "I have stored it" }).click();
    const dlg = await openMint(page);
    await expect(dlg.getByLabel("Name", { exact: true })).toHaveValue("");
    await expectNowhere(page);
  });

  test("#1351 mints operate and configure tokens only when the fold was opened and the radio chosen", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await openMint(page);
    await dlg.getByRole("button", { name: "Allow more than reading" }).click();
    await dlg.getByRole("radio", { name: /^operate/ }).check();
    await dlg.getByLabel("Name", { exact: true }).fill("runner");
    await dlg.getByRole("button", { name: "Create token" }).click();
    await reveal(page).getByRole("button", { name: "I have stored it" }).click();
    await expect(tokenRow(page, "runner").locator('[data-chip="scope"]')).toHaveText("▶operate");
    await expect(region(page, "tokens").locator("tbody tr")).toHaveCount(1);
  });
});
