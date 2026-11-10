/**
 * Issue #1319 (EPIC T, T2.4): the browser half of the suite equivalence row.
 * WRITTEN, NOT RUN: no browser was launched for this change. When it is run,
 * it goes through /Users/rom/workspace/xdg-pw-lock.sh and nowhere else, one
 * Playwright run at a time.
 *
 * Three claims, over the mock API the T2.3 (#1318) specs use:
 *  1. every command the page echoes for a suite action (suites list, policy
 *     patch, key rotate, re-encrypt) is one the CLI accepts, subcommand and
 *     flags; the flag lists are spelled here, read off the verbs the T1.5 and
 *     T2.2 issues name, so a renamed flag on either side turns this red;
 *  2. the suite select lists exactly the two shipped suites, each with its
 *     status, the default marked, row by row;
 *  3. the rotate-across-suites flow in the browser (choose age-pq/v1, rotate,
 *     download the bundle, confirm) ends with the key registry the CLI flow
 *     ends with in core/cmd/retnd/suite_equivalence_1319_test.go: age/v1
 *     superseded and age-pq/v1 active. The browser reads the mock's state and
 *     the CLI reads a real journal, so this compares the two against one
 *     expected state, not one journal against the other.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const SCENARIO = "encryption-suites";

/** Subcommand -> the flags its verb declares (plus the global --config). */
const CLI_FLAGS: Record<string, string[]> = {
  "encryption suites": ["--json", "--config"],
  "encryption policy patch": ["--mode", "--managed", "--suite", "--json", "--config"],
  "encryption key rotate": ["--suite", "--json", "--config"],
  "encryption reencrypt": ["--suite", "--key", "--deprecated", "--to", "--dry-run", "--json", "--config"],
};

/** The two suites this epic ships, spelled here and not read from the page. */
const SHIPPED = ["age/v1", "age-pq/v1"];

function checkEcho(echoed: string) {
  expect(echoed, `"${echoed}" is not a retnd command`).toMatch(/^retnd encryption /);
  // The subcommand is every word before the first flag; none of the four
  // verbs takes a positional argument.
  const sub = echoed.replace(/^retnd /, "").split(/\s--/)[0].trim();
  expect(Object.keys(CLI_FLAGS), `the echoed subcommand "${sub}" is not a suite verb the CLI has`).toContain(sub);
  for (const f of echoed.match(/--[a-z-]+/g) ?? []) {
    expect(CLI_FLAGS[sub], `the echoed flag ${f} is not one "retnd ${sub}" declares`).toContain(f);
  }
  return sub;
}

async function suiteCard(page: Page) {
  await open(page, "/settings", SCENARIO);
  const card = page.locator("[data-encryption-card]");
  await expect(card).toBeVisible();
  return card;
}

test.describe("#1319 suite equivalence, browser half", () => {
  test("the suite select lists the shipped suites with status and the default marked", async ({ page }) => {
    const card = await suiteCard(page);
    const select = card.getByRole("combobox", { name: "Suite" });
    await select.click();
    const options = page.getByRole("option");
    const texts = await options.allInnerTexts();
    // Row by row: one option per shipped suite and nothing else.
    expect(texts.length).toBe(SHIPPED.length);
    for (const id of SHIPPED) {
      const row = texts.find((t) => t.includes(id));
      expect(row, `no option for ${id} in ${JSON.stringify(texts)}`).toBeTruthy();
      expect(row!).toMatch(/Default|Supported|Deprecated|Retired/);
    }
    expect(texts.filter((t) => /Default/.test(t))).toHaveLength(1);
    expect(texts.find((t) => t.includes("age-pq/v1"))!).toMatch(/Default/);
    // Neither a marketing word nor a strength the code cannot state.
    expect(texts.join("\n")).not.toMatch(/military|256-bit/i);
  });

  test("every suite command the page echoes is one the CLI accepts", async ({ page }) => {
    const card = await suiteCard(page);
    const seen = new Set<string>();
    const collect = async () => {
      for (const t of await page.getByText(/^retnd encryption /).allInnerTexts()) seen.add(checkEcho(t.trim()));
    };
    await collect();
    await card.getByRole("combobox", { name: "Suite" }).click();
    await page.getByRole("option", { name: /age-pq\/v1/ }).click();
    await collect();
    const dlg = page.getByRole("dialog");
    if (await dlg.isVisible()) await collect();
    await open(page, "/", SCENARIO);
    const reencrypt = page.getByRole("button", { name: /Re-encrypt under current suite/ });
    if (await reencrypt.isVisible()) {
      await reencrypt.click();
      await collect();
    }
    // The control: the page echoed something for each of the four verbs, so
    // an empty loop above is not a pass.
    expect([...seen].sort()).toEqual(Object.keys(CLI_FLAGS).sort());
  });

  test("the rotate-across-suites flow ends with the registry the CLI flow ends with", async ({ page }) => {
    const card = await suiteCard(page);
    await card.getByRole("combobox", { name: "Suite" }).click();
    await page.getByRole("option", { name: /age-pq\/v1/ }).click();
    const dlg = page.getByRole("dialog");
    await expect(dlg).toBeVisible();
    await dlg.getByRole("button", { name: "Rotate key" }).click();
    // The degraded state is visible between the rotation and the confirm.
    await expect(page.getByText(/No confirmed recovery bundle/).first()).toBeVisible();
    const download = page.waitForEvent("download");
    await dlg.getByRole("button", { name: "Download bundle" }).click();
    await download;
    await dlg.getByRole("button", { name: "Confirm" }).click();
    await expect(dlg).toBeHidden();

    const keys = page.getByRole("table", { name: /Keys/ });
    await expect(keys).toBeVisible();
    const rows = await keys.getByRole("row").allInnerTexts();
    const bySuite: Record<string, string> = {};
    for (const r of rows) {
      const id = SHIPPED.find((s) => r.includes(s));
      const status = r.match(/\b(active|superseded|retired)\b/i)?.[1]?.toLowerCase();
      if (id && status) bySuite[id] = status;
    }
    // The same expected map suite_equivalence_1319_test.go asserts on the CLI.
    expect(bySuite).toEqual({ "age/v1": "superseded", "age-pq/v1": "active" });
  });
});
