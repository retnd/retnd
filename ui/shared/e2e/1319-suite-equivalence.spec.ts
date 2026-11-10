/**
 * Issue #1319 (EPIC T, T2.4): the browser half of the suite equivalence row.
 * WRITTEN, NOT RUN: no browser was launched for this change. When it is run,
 * it goes through /Users/rom/workspace/xdg-pw-lock.sh and nowhere else, one
 * Playwright run at a time.
 *
 * #1318's specs prove what each screen shows. This one proves the browser and
 * the terminal are the same product, row by row:
 *  1. every command the page echoes for a suite action (suites list, policy
 *     patch, key rotate, re-encrypt) is one the CLI accepts, subcommand and
 *     flags; the flag lists are spelled here, read off core/cmd/retnd/encryption.go,
 *     so a renamed flag on either side turns this red;
 *  2. the suite group lists the two shipped suites with the statuses the
 *     registry gives them (age/v1 supported, age-pq/v1 supported and default),
 *     and anything else it lists is a mock-only suite that cannot be chosen;
 *  3. the rotate-across-suites flow in the browser echoes, in order, exactly
 *     the writes the CLI flow in core/cmd/retnd/suite_equivalence_1319_test.go
 *     runs (policy patch --suite age-pq/v1, then key rotate --suite age-pq/v1).
 *     That test proves those writes end with age/v1 superseded and age-pq/v1
 *     active on both CLI routes; the browser runs over the mock, so it can only
 *     prove it takes the same steps, which is what this claims and no more.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, spaGo } from "./helpers";

/** Subcommand -> the flags its verb declares (plus the global --config). */
const CLI_FLAGS: Record<string, string[]> = {
  "encryption suites": ["--json", "--config"],
  "encryption policy patch": ["--mode", "--managed", "--external-file", "--external-env", "--external-command", "--suite", "--json", "--config"],
  "encryption key rotate": ["--suite", "--out", "--digest", "--bundle", "--json", "--config"],
  "encryption reencrypt": ["--suite", "--key", "--deprecated", "--to", "--dry-run", "--json", "--config"],
};

/** The CLI flow's two writes, in order, as suite_equivalence_1319_test.go runs them. */
const CLI_WRITES = ["retnd encryption policy patch --suite age-pq/v1", "retnd encryption key rotate --suite age-pq/v1"];

function subcommandOf(echoed: string): string {
  // Every word before the first flag; none of the four verbs takes an operand.
  return echoed.replace(/^retnd /, "").split(/\s--/)[0].trim();
}

function checkEcho(echoed: string): string | null {
  const sub = subcommandOf(echoed);
  if (!(sub in CLI_FLAGS)) return null; // another verb's echo (key create, export, confirm)
  for (const f of echoed.match(/--[a-z-]+/g) ?? []) {
    expect(CLI_FLAGS[sub], `the echoed flag ${f} is not one "retnd ${sub}" declares`).toContain(f);
  }
  return sub;
}

async function echoes(page: Page): Promise<string[]> {
  return (await page.getByText(/^retnd encryption /).allInnerTexts()).map((t) => t.trim());
}

async function card(page: Page, scenario: string) {
  await open(page, "/settings", scenario);
  const c = page.locator("[data-encryption-card]");
  await expect(c).toBeVisible();
  return c;
}

test.describe("#1319 suite equivalence, browser half", () => {
  test("the suite group lists the shipped suites with the registry's statuses", async ({ page }) => {
    const c = await card(page, "encryption-suite-change");
    const group = c.getByRole("radiogroup", { name: "Suite" });
    const options = group.locator("[data-suite-option]");
    const ids = await options.evaluateAll((els) => els.map((e) => e.getAttribute("data-suite-option") ?? ""));
    expect(ids).toEqual(expect.arrayContaining(["age/v1", "age-pq/v1"]));
    await expect(group.locator('[data-suite-option="age/v1"]')).toHaveAttribute("data-suite-status", "supported");
    await expect(group.locator('[data-suite-option="age-pq/v1"]')).toHaveAttribute("data-suite-status", "supported");
    await expect(group.locator('[data-suite-option="age-pq/v1"]').getByText("Default", { exact: true })).toBeVisible();
    await expect(group.getByText("Default", { exact: true })).toHaveCount(1);
    for (const id of ids.filter((i) => i !== "age/v1" && i !== "age-pq/v1")) {
      await expect(group.locator(`[data-suite-option="${id}"]`).getByRole("radio")).toBeDisabled();
    }
    expect(await group.innerText()).not.toMatch(/military|256-bit/i);
  });

  test("every suite command the page echoes is one the CLI accepts, and all four verbs are echoed", async ({ page }) => {
    const seen = new Set<string>();
    const collect = async () => {
      for (const e of await echoes(page)) {
        const sub = checkEcho(e);
        if (sub) seen.add(sub);
      }
    };
    const c = await card(page, "encryption-suite-change");
    await collect(); // the card's own `retnd encryption suites`
    await c.getByRole("radio", { name: /^age-pq\/v1/ }).check();
    const dlg = page.getByRole("dialog", { name: "Change the suite" });
    await collect(); // policy patch
    await dlg.getByRole("button", { name: "Change suite" }).click();
    await expect(dlg.locator('[data-suite-step="rotate"]')).toHaveAttribute("data-state", "now");
    await collect(); // key rotate

    await open(page, "/", "encryption-suite-lifecycle");
    await page.locator('[data-suite-health="age/v1"]').getByRole("button", { name: "Re-encrypt under current suite" }).click();
    await expect(page.getByRole("dialog", { name: "Re-encrypt under current suite" }).locator("[data-reencrypt-plan]")).toBeVisible();
    await collect(); // reencrypt, dry run and run

    // The control: something was echoed for each of the four verbs, so an
    // empty loop above is not a pass.
    expect([...seen].sort()).toEqual(Object.keys(CLI_FLAGS).sort());
  });

  test("the browser's rotate-across-suites flow echoes the CLI flow's writes, in order", async ({ page }) => {
    const c = await card(page, "encryption-suite-change");
    const order: string[] = [];
    const note = async () => {
      for (const e of await echoes(page)) {
        if (CLI_WRITES.includes(e) && !order.includes(e)) order.push(e);
      }
    };
    await c.getByRole("radio", { name: /^age-pq\/v1/ }).check();
    const dlg = page.getByRole("dialog", { name: "Change the suite" });
    await note();
    await dlg.getByRole("button", { name: "Change suite" }).click();
    await expect(dlg.locator('[data-suite-step="rotate"]')).toHaveAttribute("data-state", "now");
    await note();
    await dlg.getByRole("button", { name: "Rotate key" }).click();
    await dlg.getByRole("checkbox", { name: /I understand this file can decrypt every copy/ }).check();
    await dlg.getByRole("button", { name: "Download bundle" }).click();
    await dlg.getByRole("button", { name: "I have stored the bundle safely" }).click();
    await expect(dlg).toBeHidden();

    expect(order).toEqual(CLI_WRITES);
    // Where it ends: the policy on age-pq/v1 and nothing owed, the state the
    // CLI flow's policy answer and key list describe.
    await expect(c.getByRole("radio", { name: /^age-pq\/v1/ })).toBeChecked();
    await spaGo(page, "/");
    await expect(page.locator("[data-encryption-health]").getByText(/No active key for suite/)).toHaveCount(0);
  });
});
