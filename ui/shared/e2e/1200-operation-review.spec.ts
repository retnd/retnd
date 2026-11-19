/**
 * Issue #1200, review round: what the Operation page says for a run that is
 * running but has written nothing durable yet, a run the record does not hold
 * for the first moment, a run with no end that nothing is running, and where a
 * terminal gets the validator output the page shows. These drive the real
 * page through the production assurance seam (wire=1).
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const OPS = "/assurance/operations/";
const panel = (p: Page) => p.getByTestId("operation-progress");
const lines = (p: Page) => panel(p).getByTestId("operation-line");
const following = (p: Page) => p.getByRole("checkbox", { name: "Following" });

test.describe("#1200 a running scrub opened cold", () => {
  test("is running, with its start line, and is not 'nothing on record'", async ({ page }) => {
    await open(page, OPS + "aop_1200_scrub_running", "operation-history", { wire: true });
    await expect(panel(page)).toHaveAttribute("data-state", "running");
    await expect(page.getByTestId("operation-none")).toHaveCount(0);
    await expect(lines(page)).toHaveText(["scrub started (manual, set production/postgres-primary)"]);
    // There is something live to follow, so Following is offered.
    await expect(following(page)).toBeVisible();
    // It stays that way: a poll later it has not turned into a missing run.
    await page.waitForTimeout(2500);
    await expect(panel(page)).toHaveAttribute("data-state", "running");
    await expect(page.getByTestId("operation-none")).toHaveCount(0);
  });
});

test.describe("#1200 a run the record does not hold yet", () => {
  test("is looked for for a moment, then found, and is never called missing", async ({ page }) => {
    await open(page, OPS + "aop_1200_late", "operation-history", { wire: true });
    // The first reads find nothing. That is "looking", not "nothing on record".
    await expect(page.getByTestId("operation-looking")).toBeVisible();
    await expect(page.getByTestId("operation-none")).toHaveCount(0);
    // The record catches up and the run is shown.
    await expect(panel(page)).toHaveAttribute("data-state", "complete", { timeout: 15_000 });
    await expect(page.getByTestId("operation-none")).toHaveCount(0);
    await expect(page.getByTestId("operation-looking")).toHaveCount(0);
  });

  test("an id that never turns up is said to be missing once the grace period is over", async ({ page }) => {
    await open(page, OPS + "aop_1200_pruned", "operation-history", { wire: true });
    await expect(page.getByTestId("operation-looking")).toBeVisible();
    await expect(page.getByTestId("operation-none")).toBeVisible({ timeout: 15_000 });
    await expect(page.getByTestId("operation-looking")).toHaveCount(0);
  });
});

test.describe("#1200 a run with no end that nothing is running", () => {
  test("does not claim the process crashed: it may be running elsewhere", async ({ page }) => {
    await open(page, OPS + "aop_1200_unfinished", "operation-history", { wire: true });
    await expect(panel(page)).toHaveAttribute("data-state", "unfinished");
    const status = panel(page).getByRole("status");
    await expect(status).toContainText("no end on record");
    await expect(status).toContainText("may still be running in another process");
    await expect(status).toContainText("or the process running it stopped");
    await expect(following(page)).toHaveCount(0);
    // It never ended, so it has no output to point at.
    await expect(page.getByText("retnd assurance show aop_1200_unfinished", { exact: true })).toBeVisible();
    await expect(page.getByText(/--show-output/)).toHaveCount(0);
  });
});

test.describe("#1200 the terminal commands that match the page", () => {
  test("a finished recovery test echoes assurance show and the command for its validator output", async ({ page }) => {
    await open(page, OPS + "aop_1200_passed", "operation-history", { wire: true });
    await expect(page.getByTestId("validator-output")).toBeVisible();
    await expect(page.getByText("retnd assurance show aop_1200_passed", { exact: true })).toBeVisible();
    await expect(page.getByText("retnd recovery-test production/postgres-primary --show-output", { exact: true })).toBeVisible();
  });

  test("a scrub has no validator output, so only assurance show is echoed", async ({ page }) => {
    await open(page, OPS + "aop_1200_scrub", "operation-history", { wire: true });
    await expect(panel(page)).toHaveAttribute("data-state", "complete");
    await expect(page.getByText("retnd assurance show aop_1200_scrub", { exact: true })).toBeVisible();
    await expect(page.getByText(/--show-output/)).toHaveCount(0);
  });
});

test.describe("#1200 a cancelled run is cancelled because its end line says so", () => {
  test("the cancelled recovery test reads as cancelled", async ({ page }) => {
    await open(page, OPS + "aop_1200_cancelled", "operation-history", { wire: true });
    await expect(panel(page)).toHaveAttribute("data-state", "cancelled");
    await expect(panel(page).getByRole("status")).toHaveText("Recovery test cancelled");
  });
});
