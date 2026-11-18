/**
 * Issue #996 (EPIC N): the browser half of Suite C. WRITTEN, NOT RUN: no
 * browser was launched for this change. When it is run, it goes through
 * /Users/rom/workspace/xdg-pw-lock.sh and nowhere else.
 *
 * Two claims, over the mock API the other encryption specs use:
 *  1. a reconnect (offline, online, reload) shows the same durable state the
 *     page showed before it, for the copy's encryption chips;
 *  2. every command the page echoes for an encryption action is one the CLI
 *     accepts: the subcommand is `encryption verify`, and every flag in it is
 *     one cmdEncryptionVerify declares (--medium, --allow-egress, --json).
 *     The flag list is spelled here on purpose, read off core/cmd/retnd/encryption.go,
 *     so a renamed flag turns this red.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, row } from "./helpers";

const PG = "postgres-prod-20260828.dump.zst";
const CLI_VERIFY_FLAGS = ["--medium", "--allow-egress", "--json", "--config"];

async function copiesOf(page: Page, scenario: string) {
  await open(page, "/backups", scenario);
  await row(page, PG).click();
  const copies = page.getByRole("region", { name: "Copies" });
  await expect(copies).toBeVisible();
  return copies;
}

async function chipsOf(page: Page): Promise<string[]> {
  const copies = page.getByRole("region", { name: "Copies" });
  const cells = copies.locator("[data-encryption-cell]");
  await expect(cells.first()).toBeVisible();
  const n = await cells.count();
  const out: string[] = [];
  for (let i = 0; i < n; i++) {
    const cell = cells.nth(i);
    const medium = await cell.getAttribute("data-encryption-cell");
    const groups = await cell.getByRole("group").allInnerTexts();
    out.push(`${medium}: ${groups.join(" | ")}`);
  }
  return out;
}

test.describe("#996 browser reconnect and CLI equivalence", () => {
  test("a reconnect mid-session shows the same encryption state the page showed before", async ({ page, context }) => {
    await copiesOf(page, "encryption-on");
    const before = await chipsOf(page);
    // The control: the read found something, so equality below is not two empty lists.
    expect(before.length).toBeGreaterThan(1);
    expect(before.join("\n")).toMatch(/Encrypted/);
    expect(before.join("\n")).toMatch(/Plaintext/);

    await context.setOffline(true);
    await context.setOffline(false);
    // Opening the row navigated to the artifact path and dropped ?scenario=,
    // so a bare reload would boot the default mock. Open it again instead.
    const copies = await copiesOf(page, "encryption-on");
    await expect(copies.locator("[data-encryption-cell]")).toHaveCount(before.length);
    expect(await chipsOf(page)).toEqual(before);
  });

  test("the command a verify echoes uses only flags the CLI's verify verb declares", async ({ page }) => {
    const copies = await copiesOf(page, "encryption-on");
    await copies.getByRole("button", { name: "Verify recoverable" }).click();
    const dlg = page.getByRole("dialog", { name: /Verify recoverable/ });
    const echoed = await dlg.getByText(/retnd encryption verify /).first().innerText();
    expect(echoed).toMatch(/^retnd encryption verify \S+/);
    const flags = echoed.match(/--[a-z-]+/g) ?? [];
    expect(flags.length).toBeGreaterThan(0);
    for (const f of flags) {
      expect(CLI_VERIFY_FLAGS, `the echoed flag ${f} is not one the CLI declares`).toContain(f);
    }
  });
});
