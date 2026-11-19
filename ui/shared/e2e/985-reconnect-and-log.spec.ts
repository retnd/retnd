import { expect, test } from "@playwright/test";
import { open, measureBorders, measureContrast, setTheme } from "./helpers";

const SET = "/sets/production/postgres-primary";
const region = (page: import("@playwright/test").Page) => page.getByRole("region", { name: "Assurance" });
// The set's own terminal, where a run's output lives since #1176.
const setLines = (page: import("@playwright/test").Page) => page.locator('#set-terminal [role="log"] > div');

// The engine's stage ids (obs.RecoveryStages). A stage the engine does not
// name must never appear, so a UI that invents "digest" or "validator" fails.
const ENGINE_STAGES = [
  "resolving_copy", "restoring_archive", "reading", "verifying", "decrypting", "writing_target",
  "validating", "cleaning_temporary_files", "complete"
];

test.describe("#985 reconnect and continuity", () => {
  test("a reload mid-recovery-test restores the stage and bytes and starts no second run", async ({ page }) => {
    await open(page, SET, "assurance-slow-test");
    const section = region(page);
    await section.getByRole("button", { name: "Run recovery test" }).click();
    const prog = section.getByTestId("operation-progress");
    await expect(prog).toHaveAttribute("data-stage", /reading|verifying/, { timeout: 15_000 });
    const before = await prog.getAttribute("data-bytes");
    expect(Number(before)).toBeGreaterThan(0);

    await page.reload();
    const after = region(page).getByTestId("operation-progress");
    await expect(after).toBeVisible();
    await expect(after).toHaveAttribute("data-stage", /reading|verifying/);
    expect(Number(await after.getAttribute("data-bytes"))).toBeGreaterThanOrEqual(Number(before));
    const state = JSON.parse((await page.evaluate(() => sessionStorage.getItem("retnd.mock.assurance"))) ?? "{}");
    expect(state.starts).toBe(1);
  });

  test("a dropped connection replays from its cursor with no duplicate terminal lines", async ({ page }) => {
    await open(page, SET, "assurance-slow-test");
    const section = region(page);
    await section.getByRole("button", { name: "Run recovery test" }).click();
    await expect(section.getByTestId("operation-progress")).toBeVisible();
    await page.evaluate(() => sessionStorage.setItem("retnd.mock.assurance.drop", "1"));
    await expect(section.getByTestId("operation-progress")).toHaveAttribute("data-stage", /verifying|validating/, { timeout: 20_000 });
    // a replay from the cursor adds no second copy of any line the terminal holds
    const lines = (await setLines(page).allTextContents()).map((l) => l.replace(/^\d\d:\d\d:\d\d\s*/, ""));
    expect(new Set(lines).size).toBe(lines.length);
    expect(lines.filter((l) => l.startsWith("recovery test stage:")).length).toBeGreaterThan(0);
  });

  test("a recovery test goes through the engine's named stages, in its order, and no others", async ({ page }) => {
    await open(page, SET, "assurance-slow-test");
    const section = region(page);
    await section.getByRole("button", { name: "Run recovery test" }).click();
    const prog = section.getByTestId("operation-progress");
    const seen: string[] = [];
    await expect.poll(async () => {
      const s = await prog.getAttribute("data-stage");
      if (s && seen[seen.length - 1] !== s) seen.push(s);
      return s;
    }, { timeout: 45_000, intervals: [250] }).toBe("complete");
    for (const s of seen) expect(ENGINE_STAGES, s).toContain(s);
    // order follows the engine's: re-read, digest check, script, cleanup
    const idx = seen.map((s) => ENGINE_STAGES.indexOf(s));
    expect(idx).toEqual([...idx].sort((a, b) => a - b));
    expect(seen).toEqual(expect.arrayContaining(["reading", "verifying", "validating"]));
    // labels are the engine's labels
    await expect(setLines(page).filter({ hasText: "recovery test stage: Verifying" }).first()).toBeVisible();
    await expect(setLines(page).filter({ hasText: /recovery test stage: Validating$/ })).toHaveCount(1);
  });

  test("a finished recovery test updates the Recovery claim and leaves Content verified exactly as it was", async ({ page }) => {
    await open(page, SET, "assurance-slow-test");
    const section = region(page);
    const content = section.locator('[data-claim="content"]');
    const recovery = section.locator('[data-claim="recovery"]');
    await expect(content).toContainText("Content verified");
    await expect(content).toContainText("3d ago");
    await expect(recovery).toContainText("12d ago");
    await section.getByRole("button", { name: "Run recovery test" }).click();
    await expect(recovery).toContainText("just now", { timeout: 45_000 });
    await expect(recovery).toHaveAttribute("data-state", "pass");
    // the content claim is the scrub's, so a recovery test does not refresh it
    await expect(content).toContainText("3d ago");
  });

  test("a recovery test whose cleanup failed is its own state, not a clean pass", async ({ page }) => {
    await open(page, SET, "assurance-cleanup-fails");
    const section = region(page);
    await section.getByRole("button", { name: "Run recovery test" }).click();
    const recovery = section.locator('[data-claim="recovery"]');
    await expect(recovery).toHaveAttribute("data-state", "cleanup_failed", { timeout: 45_000 });
    await expect(recovery).toContainText("Cleanup failed: staged copy left behind");
    await expect(recovery).not.toContainText("Recovery test passed");
  });

  test("every named stage reaches the docked terminal exactly once", async ({ page }) => {
    await open(page, SET, "assurance-slow-test");
    const section = region(page);
    await section.getByRole("button", { name: "Run recovery test" }).click();
    const log = page.getByLabel("Terminal", { exact: true }).getByRole("log");
    await expect(log).toContainText("recovery test stage: Verifying", { timeout: 20_000 });
    // a few more polls, and a dropped connection in the middle
    await page.evaluate(() => sessionStorage.setItem("retnd.mock.assurance.drop", "1"));
    await page.waitForTimeout(3000);
    // one dedicated line per stage, in words, and the id is not printed as a
    // key=value pair
    for (const label of ["Resolving copy", "Reading", "Verifying"]) {
      await expect(log.getByText("recovery test stage: " + label, { exact: true })).toHaveCount(1);
    }
  });

  test("the operation detail view follows the live stage and can cancel", async ({ page }) => {
    await open(page, SET, "assurance-slow-test");
    const section = region(page);
    await section.getByRole("button", { name: "Run recovery test" }).click();
    await section.getByRole("link", { name: "Operation detail" }).click();
    await expect(page.getByRole("checkbox", { name: "Following" })).toBeChecked();
    await page.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByTestId("operation-progress")).toHaveAttribute("data-state", "cancelled");
    // the terminal twin of Cancel is `retnd assurance cancel`, and the CLI has
    // no `operations cancel` verb for a read
    await expect(page.getByText("retnd operations cancel", { exact: false })).toHaveCount(0);
  });
});

test.describe("#985 validator output", () => {
  test("OSC and window-control sequences are displayed inert and truncation is a marker", async ({ page }) => {
    await open(page, SET, "assurance-validator-output");
    const title = await page.title();
    await region(page).getByRole("button", { name: "Show validator output" }).click();
    const log = page.getByTestId("validator-log");
    await expect(log).toContainText("\\x1b]0;pwned\\x07");
    await expect(log).toContainText("\\x1b[2J");
    expect(await page.title()).toBe(title);
    const marker = page.getByTestId("truncation-marker");
    await expect(marker).toHaveCount(1);
    await expect(marker).toContainText(/truncated/i);
    // the marker is drawn outside the content stream, and a forged marker line
    // inside the validator's own output is content, not a marker
    expect(await marker.evaluate((m, l) => l!.contains(m), await log.elementHandle())).toBe(false);
    await expect(log.getByText("[output truncated]")).toBeVisible();
    await expect(log.locator('[data-testid="truncation-marker"]')).toHaveCount(0);
  });

  test("says the engine recorded the verdict, not the script, and what the script printed is plain text under it", async ({ page }) => {
    await open(page, SET, "assurance-validator-output");
    await region(page).getByRole("button", { name: "Show validator output" }).click();
    const facts = page.getByTestId("validator-facts");
    await expect(facts).toContainText(/newest recovery test of this set failed, on the local copy of production\/postgres-primary\/postgres-prod-20260828\.dump\.zst/);
    await expect(facts).toContainText(/The engine records that verdict, not the script/);
    // reading what a script printed changes no claim: the Recovery chip is still
    // the one the engine recorded twelve days ago
    const recovery = page.getByRole("region", { name: "Assurance" }).locator('[data-claim="recovery"]');
    await expect(recovery).toContainText("12d ago");
    await expect(recovery).not.toContainText("just now");
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`#985 the operation panel and the validator log meet contrast in the ${theme} theme`, async ({ page }) => {
    await open(page, SET, "assurance-validator-output");
    await expect(page.locator("main")).toBeVisible();
    await setTheme(page, theme);
    const section = region(page);
    await section.getByRole("button", { name: "Run recovery test" }).click();
    await expect(section.getByTestId("operation-progress")).toBeVisible();
    await section.getByRole("button", { name: "Show validator output" }).click();
    await expect(page.getByTestId("validator-log")).toBeVisible();
    expect(await measureContrast(page, `operation panel and validator log, ${theme}`, '[aria-label="Assurance"]')).toEqual([]);
    expect(await measureBorders(page, `progress bar, ${theme}`, '[aria-label="Assurance"] .aop__bar')).toEqual([]);
  });
}
