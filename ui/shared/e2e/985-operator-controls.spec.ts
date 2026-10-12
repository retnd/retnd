import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureBorders, measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #985: Scrub now, Cancel and Show validator output on a production
 * client. Every spec here opens the app with `wire=1`, which swaps the mock's
 * own assurance seam for realAssurance (the code a built app runs) reading
 * the mock's routes, so these drive the wiring and not a stand-in for it.
 */
const SET = "/sets/production/postgres-primary";
const region = (page: Page) => page.getByRole("region", { name: "Assurance" });
// The lines this issue adds, not the dock's own chrome (its timestamps and
// bracketed names are older than this work and are measured by its own suite).
const TERMINAL_LINES = '[aria-label="Terminal"] [class^="activity-log__line--"]';
const terminal = (page: Page) => page.getByLabel("Terminal", { exact: true }).getByRole("log");

test.describe("#985 Scrub now", () => {
  test("starts a scrub, follows it to a finish, and prints its terminal twins beside it", async ({ page }) => {
    await open(page, SET, "default", { wire: true });
    const section = region(page);
    await expect(section.getByTestId("no-run-route")).toHaveCount(0);
    await expect(section.getByText("retnd scrub production/postgres-primary", { exact: true })).toBeVisible();
    await expect(section.getByText("retnd recovery-test production/postgres-primary --show-output", { exact: true })).toBeVisible();

    await section.getByRole("button", { name: "Scrub now" }).click();
    const prog = section.getByTestId("operation-progress");
    await expect(prog).toHaveAttribute("data-state", "running");
    await expect(section.getByTestId("stage-label")).toHaveText("Checking the set's copies");
    // the operation id is the one Cancel takes, and the twin names it
    await expect(section.getByText(/^retnd assurance cancel aop_/)).toBeVisible();
    await expect(section.getByRole("button", { name: "Scrub now" })).toBeDisabled();

    await expect(prog).toHaveAttribute("data-state", "complete", { timeout: 20_000 });
    await expect(prog.getByRole("status")).toHaveText("Scrub finished");
    await expect(section.getByTestId("operation-line").last()).toContainText("scrub pass finished (manual, set production/postgres-primary)");
    // a finished run has nothing left to cancel
    await expect(section.getByRole("button", { name: "Cancel" })).toHaveCount(0);
  });

  test("a scrub that finds a copy wrong replaces the pass, and the failing copy is a line of it and of the terminal", async ({ page }) => {
    await open(page, SET, "assurance-scrub-fails", { wire: true });
    const section = region(page);
    const content = section.locator('[data-claim="content"]');
    await expect(content).toHaveAttribute("data-state", "pass");
    await section.getByRole("button", { name: "Scrub now" }).click();
    await expect(section.getByTestId("operation-progress")).toHaveAttribute("data-state", "failed", { timeout: 20_000 });
    await expect(section.getByTestId("operation-progress").getByRole("status")).toHaveText("Scrub failed");
    await expect(section.getByTestId("operation-line").filter({ hasText: "scrub did not pass:" })).toContainText("sha256 differs from the one recorded at ingest");
    await expect(content).toHaveAttribute("data-state", "failed", { timeout: 15_000 });
    await expect(section.getByText("Content verified")).toHaveCount(0);
    await expect(terminal(page)).toContainText("scrub did not pass: postgres-prod-20260828.dump.zst on local (content check): sha256 differs");
    await expect(terminal(page)).toContainText("scrub pass found problems (manual, set production/postgres-primary): 1 checked, 0 passed, 1 failed");
  });

  test("while a scrub runs, Scrub now and Run recovery test are spent", async ({ page }) => {
    await open(page, SET, "default", { wire: true });
    const section = region(page);
    await section.getByRole("button", { name: "Scrub now" }).click();
    await expect(section.getByRole("button", { name: "Scrub now" })).toBeDisabled();
    await expect(section.getByRole("button", { name: "Run recovery test" })).toBeDisabled();
  });
});

test.describe("#985 Cancel", () => {
  test("a scrub says Cancelling at once, ends as Cancelled, and leaves the pass chip as it was", async ({ page }) => {
    await open(page, SET, "default", { wire: true });
    const section = region(page);
    const content = section.locator('[data-claim="content"]');
    await expect(content).toHaveAttribute("data-state", "pass");
    await section.getByRole("button", { name: "Scrub now" }).click();
    await section.getByRole("button", { name: "Cancel" }).click();
    const prog = section.getByTestId("operation-progress");
    await expect(prog).toHaveAttribute("data-cancelling", "true");
    await expect(section.getByTestId("operation-cancelling")).toHaveText("Cancelling");
    await expect(section.getByRole("button", { name: "Cancel" })).toBeDisabled();
    await expect(prog).toHaveAttribute("data-state", "cancelled", { timeout: 20_000 });
    await expect(prog.getByRole("status")).toHaveText("Scrub cancelled");
    // a cancelled scrub proved nothing: the previous answer stays
    await expect(content).toHaveAttribute("data-state", "pass");
    await expect(content).toContainText("Content verified");
  });

  test("a recovery test ends as Cancelled, its end line is a warning and not an error or a pass", async ({ page }) => {
    await open(page, SET, "assurance-slow-test", { wire: true });
    const section = region(page);
    const recovery = section.locator('[data-claim="recovery"]');
    // the chip is read once the claims have loaded, not while it says Unknown
    await expect(recovery).not.toHaveAttribute("data-state", "unknown");
    const before = await recovery.textContent();
    await section.getByRole("button", { name: "Run recovery test" }).click();
    await expect(section.getByTestId("operation-progress")).toHaveAttribute("data-state", "running");
    await section.getByRole("button", { name: "Cancel" }).click();
    await expect(section.getByTestId("operation-progress")).toHaveAttribute("data-state", "cancelled", { timeout: 15_000 });
    await expect(section.getByTestId("operation-progress").getByRole("status")).toHaveText("Recovery test cancelled");
    // a cancelled test proved nothing: the Recovery claim is exactly as it was
    await expect(recovery).toHaveText(before!);
    const end = terminal(page).getByText(/recovery test did not complete: .*cancelled before it finished/);
    await expect(end).toBeVisible();
    await expect(end).toHaveClass(/activity-log__line--warn/);
  });

  test("the operation detail page cancels too, and prints the twin", async ({ page }) => {
    await open(page, SET, "assurance-slow-test", { wire: true });
    await region(page).getByRole("button", { name: "Run recovery test" }).click();
    await region(page).getByRole("link", { name: "Operation detail" }).click();
    await expect(page.getByText(/^retnd assurance cancel aop_/)).toBeVisible();
    await page.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByTestId("operation-progress")).toHaveAttribute("data-state", "cancelled", { timeout: 15_000 });
  });

  test("a reload keeps the operation detail: a recovery test is found on the feed by its id", async ({ page }) => {
    await open(page, SET, "assurance-slow-test", { wire: true });
    await region(page).getByRole("button", { name: "Run recovery test" }).click();
    const id = await region(page).getByTestId("operation-progress").getAttribute("data-operation-id");
    expect(id).toMatch(/^aop_/);
    await open(page, "/assurance/operations/" + id, "assurance-slow-test", { wire: true });
    const prog = page.getByTestId("operation-progress");
    await expect(prog).toHaveAttribute("data-operation-id", id!);
    await expect(prog).toHaveAttribute("data-state", "running");
    await expect(prog.getByText("Recovery test", { exact: true }).first()).toBeVisible();
  });
});

test.describe("#985 Show validator output", () => {
  test("reads the stored output, draws it inert with the marker outside it, and prints its twin", async ({ page }) => {
    await open(page, SET, "assurance-validator-output", { wire: true });
    const title = await page.title();
    await region(page).getByRole("button", { name: "Show validator output" }).click();
    const log = page.getByTestId("validator-log");
    await expect(log).toContainText("\\x1b]0;pwned\\x07");
    await expect(log).toContainText("\\x1b[2J");
    expect(await page.title()).toBe(title);
    // a validator that prints the marker's words has printed a line of output
    await expect(log.getByText("[output truncated]")).toBeVisible();
    const marker = page.getByTestId("truncation-marker");
    await expect(marker).toHaveCount(1);
    await expect(marker).toContainText("output truncated: 41873 bytes were dropped");
    expect(await marker.evaluate((m, l) => l!.contains(m), await log.elementHandle())).toBe(false);
    // and the marker is last
    expect(await page.getByTestId("validator-output").evaluate((el) => (el.lastElementChild as HTMLElement).dataset.testid)).toBe("truncation-marker");
    await expect(region(page).getByText("retnd recovery-test production/postgres-primary --show-output", { exact: true })).toBeVisible();
  });

  test("before any test has recorded output it says so and draws no log", async ({ page }) => {
    await open(page, SET, "default", { wire: true });
    await region(page).getByRole("button", { name: "Show validator output" }).click();
    await expect(region(page).getByText("No recovery test has run for this set yet.")).toBeVisible();
    await expect(page.getByTestId("validator-log")).toHaveCount(0);
  });
});

test.describe("#985 following an operation", () => {
  test("a recovery test goes through the engine's stages in order, and the terminal says the same in words", async ({ page }) => {
    await open(page, SET, "assurance-slow-test", { wire: true });
    const section = region(page);
    await section.getByRole("button", { name: "Run recovery test" }).click();
    await expect(section.getByTestId("operation-progress")).toHaveAttribute("data-state", "complete", { timeout: 60_000 });
    const lines = await section.getByTestId("operation-line").allTextContents();
    expect(lines).toEqual([
      "Recovery test started", "Resolving copy", "Reading", "Verifying", "Validating", "Cleaning temporary files", "Complete", "Recovery test finished"
    ]);
    const stages = [
      "Resolving copy", "Reading", "Verifying", "Validating", "Cleaning temporary files", "Complete"
    ];
    await expect(terminal(page)).toContainText("recovery test passed: postgres-prod-20260828.dump.zst", { timeout: 30_000 });
    const at = (await terminal(page).textContent()) ?? "";
    let from = 0;
    for (const s of stages) {
      const i = at.indexOf("recovery test stage: " + s, from);
      expect(i, s).toBeGreaterThanOrEqual(from);
      from = i;
    }
  });

  test("a reload mid-run re-attaches to the running test and starts no second one", async ({ page }) => {
    await open(page, SET, "assurance-slow-test", { wire: true });
    await region(page).getByRole("button", { name: "Run recovery test" }).click();
    const id = await region(page).getByTestId("operation-progress").getAttribute("data-operation-id");
    await page.reload();
    const prog = region(page).getByTestId("operation-progress");
    await expect(prog).toHaveAttribute("data-operation-id", id!);
    await expect(prog).toHaveAttribute("data-state", "running");
    const state = JSON.parse((await page.evaluate(() => sessionStorage.getItem("retnd.mock.assurance"))) ?? "{}");
    expect(state.starts).toBe(1);
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`#985 the operator controls and their statuses meet contrast in the ${theme} theme`, async ({ page }) => {
    await open(page, SET, "assurance-validator-output", { wire: true });
    await setTheme(page, theme);
    const section = region(page);
    // running scrub with Cancel and the twins
    await section.getByRole("button", { name: "Scrub now" }).click();
    await expect(section.getByTestId("operation-progress")).toHaveAttribute("data-state", "running");
    expect(await measureContrast(page, `scrub running, Cancel and twins, ${theme}`, '[aria-label="Assurance"]')).toEqual([]);
    expect(await measureBorders(page, `scrub running controls, ${theme}`, '[aria-label="Assurance"] .btn, [aria-label="Assurance"] .aop__bar')).toEqual([]);
    // cancelling, then cancelled
    await section.getByRole("button", { name: "Cancel" }).click();
    await expect(section.getByTestId("operation-cancelling")).toBeVisible();
    expect(await measureContrast(page, `scrub cancelling, ${theme}`, '[aria-label="Assurance"]')).toEqual([]);
    await expect(section.getByTestId("operation-progress")).toHaveAttribute("data-state", "cancelled", { timeout: 20_000 });
    expect(await measureContrast(page, `scrub cancelled, ${theme}`, '[aria-label="Assurance"]')).toEqual([]);
    // the validator log, its facts and the truncation marker
    await section.getByRole("button", { name: "Show validator output" }).click();
    await expect(page.getByTestId("validator-log")).toBeVisible();
    expect(await measureContrast(page, `validator log, facts and marker, ${theme}`, '[data-testid="validator-output"]')).toEqual([]);
    expect(await measureBorders(page, `validator marker, ${theme}`, '[data-testid="validator-output"] .avlog__marker')).toEqual([]);
    // the new lines in the terminal, one per tone: ok, warn
    expect(await measureContrast(page, `activity lines, ${theme}`, TERMINAL_LINES)).toEqual([]);
  });

  test(`#985 a failed scrub and a finished test meet contrast in the ${theme} theme`, async ({ page }) => {
    // a scrub (4s) and then a whole recovery test (25s) are followed to their ends
    test.setTimeout(120_000);
    await open(page, SET, "assurance-scrub-fails", { wire: true });
    await setTheme(page, theme);
    const section = region(page);
    await section.getByRole("button", { name: "Scrub now" }).click();
    await expect(section.getByTestId("operation-progress")).toHaveAttribute("data-state", "failed", { timeout: 20_000 });
    expect(await measureContrast(page, `scrub failed, ${theme}`, '[aria-label="Assurance"]')).toEqual([]);
    expect(await measureContrast(page, `activity lines after a failed scrub, ${theme}`, TERMINAL_LINES)).toEqual([]);
    await section.getByRole("button", { name: "Run recovery test" }).click();
    await expect(section.getByTestId("operation-progress")).toHaveAttribute("data-state", "complete", { timeout: 60_000 });
    expect(await measureContrast(page, `recovery test finished, ${theme}`, '[aria-label="Assurance"]')).toEqual([]);
    expect(await measureContrast(page, `activity lines after a recovery test, ${theme}`, TERMINAL_LINES)).toEqual([]);
  });
}
