/**
 * Issue #1200: the Operation page (/assurance/operations/:id) only drew lines
 * it got from the live activity tail. A run that had left the tail showed "no
 * longer on the live feed", a stale "Stage 1 of 5", a 0% bar and an empty
 * black terminal strip, and a cold open of a finished run could not find it
 * at all. These drive the real page, through the production assurance seam
 * (wire=1), against the mock's durable record.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const SET = "/sets/production/postgres-primary";
const OPS = "/assurance/operations/";
const region = (p: Page) => p.getByRole("region", { name: "Assurance" });
const panel = (p: Page) => p.getByTestId("operation-progress");
const lines = (p: Page) => panel(p).getByTestId("operation-line");
const following = (p: Page) => p.getByRole("checkbox", { name: "Following" });

/** Every terminal strip on the page holds at least one line. */
async function noEmptyTerminal(page: Page) {
  const counts = await page.locator(".aop__lines").evaluateAll((els) => els.map((e) => e.querySelectorAll("li").length));
  expect(counts.filter((n) => n === 0), "an empty terminal strip was drawn").toEqual([]);
}

test.describe("#1200 a recovery test that left the live tail", () => {
  test("ended off the tail: its start, stage and end lines and its outcome are shown", async ({ page }) => {
    test.setTimeout(90_000);
    await open(page, SET, "operation-off-tail", { wire: true });
    await region(page).getByRole("button", { name: "Run recovery test" }).click();
    await region(page).getByRole("link", { name: "Operation detail" }).click();
    await expect(panel(page)).toHaveAttribute("data-state", "running");
    // While it runs, the record already holds its start: never an empty strip.
    await expect(lines(page).first()).toHaveText("Recovery test started", { timeout: 5_000 });
    await noEmptyTerminal(page);
    // There is something live to follow, so Following is offered.
    await expect(following(page)).toBeVisible();

    await expect(panel(page)).toHaveAttribute("data-state", "complete", { timeout: 60_000 });
    await expect(panel(page).getByRole("status")).toHaveText("Recovery test finished");
    await expect(lines(page).first()).toHaveText("Recovery test started");
    await expect(lines(page).last()).toHaveText("Recovery test finished");
    await expect(lines(page).filter({ hasText: /^Complete$/ })).toHaveCount(1);
    await expect(panel(page).getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
    await expect(panel(page).getByTestId("stage-count")).toHaveCount(0);
    await expect(page.getByText(/no longer on the live feed/)).toHaveCount(0);
    await expect(following(page)).toHaveCount(0);
    await noEmptyTerminal(page);
  });

  test("a reload of the same run, once it ended, reads the same lines back", async ({ page }) => {
    test.setTimeout(90_000);
    await open(page, SET, "operation-off-tail", { wire: true });
    await region(page).getByRole("button", { name: "Run recovery test" }).click();
    const id = await region(page).getByTestId("operation-progress").getAttribute("data-operation-id");
    expect(id).toMatch(/^aop_/);
    await open(page, OPS + id, "operation-off-tail", { wire: true });
    await expect(panel(page)).toHaveAttribute("data-state", "complete", { timeout: 60_000 });
    await page.reload();
    await expect(panel(page)).toHaveAttribute("data-state", "complete");
    await expect(panel(page)).toHaveAttribute("data-operation-id", id!);
    await expect(lines(page)).toHaveText(["Recovery test started", "Complete", "Recovery test finished"]);
    await expect(panel(page).getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
    await expect(following(page)).toHaveCount(0);
  });
});

test.describe("#1200 a cold open of a finished run reads the durable record", () => {
  test("a passed recovery test: lines, outcome, a full bar, and its validator output", async ({ page }) => {
    await open(page, OPS + "aop_1200_passed", "operation-history", { wire: true });
    await expect(panel(page)).toHaveAttribute("data-state", "complete");
    await expect(panel(page)).toHaveAttribute("data-stage", "complete");
    await expect(panel(page).getByRole("status")).toHaveText("Recovery test finished");
    await expect(lines(page)).toHaveText(["Recovery test started", "Complete", "Recovery test finished"]);
    await expect(panel(page).getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
    await expect(panel(page).getByTestId("stage-count")).toHaveCount(0);
    await expect(page.getByText("Stage 1 of 5")).toHaveCount(0);
    await expect(following(page)).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Cancel" })).toHaveCount(0);
    // The set's newest output is this run's, so it is shown here.
    await expect(page.getByTestId("validator-output")).toBeVisible();
    await expect(page.getByTestId("validator-log")).toContainText("pg-restore-check: ok");
    // The terminal twin that prints the same lines.
    await expect(page.getByText("retnd assurance show aop_1200_passed", { exact: true })).toBeVisible();
    await noEmptyTerminal(page);
  });

  test("a failed recovery test: the stage it reached, its reason, and no one else's output", async ({ page }) => {
    await open(page, OPS + "aop_1200_failed", "operation-history", { wire: true });
    await expect(panel(page)).toHaveAttribute("data-state", "failed");
    await expect(panel(page)).toHaveAttribute("data-stage", "verifying");
    await expect(panel(page).getByRole("status")).toHaveText("Recovery test failed");
    await expect(lines(page)).toHaveText([
      "Recovery test started", "Verifying", "Recovery test did not pass: sha256 differs from the one recorded at ingest"
    ]);
    // verifying is the third of the five working stages: 2 of 5 done
    await expect(panel(page).getByTestId("stage-count")).toHaveText("Stage 3 of 5");
    await expect(panel(page).getByRole("progressbar")).toHaveAttribute("aria-valuenow", "40");
    await expect(following(page)).toHaveCount(0);
    // The set's newest output belongs to a later run, not this one.
    await expect(page.getByTestId("validator-output")).toHaveCount(0);
    await noEmptyTerminal(page);
  });

  test("a cancelled recovery test reads as cancelled, not as a failure", async ({ page }) => {
    await open(page, OPS + "aop_1200_cancelled", "operation-history", { wire: true });
    await expect(panel(page)).toHaveAttribute("data-state", "cancelled");
    await expect(panel(page).getByRole("status")).toHaveText("Recovery test cancelled");
    await expect(lines(page)).toHaveText(["Recovery test started", "Reading", "Recovery test cancelled"]);
    await expect(panel(page).getByTestId("stage-count")).toHaveText("Stage 2 of 5");
    await expect(panel(page).getByRole("progressbar")).toHaveAttribute("aria-valuenow", "20");
    await expect(following(page)).toHaveCount(0);
    await expect(page.getByTestId("validator-output")).toHaveCount(0);
    await noEmptyTerminal(page);
  });

  test("a scrub: its pass line and its outcome", async ({ page }) => {
    await open(page, OPS + "aop_1200_scrub", "operation-history", { wire: true });
    await expect(panel(page)).toHaveAttribute("data-state", "complete");
    await expect(panel(page).getByRole("status")).toHaveText("Scrub finished");
    await expect(lines(page)).toHaveCount(1);
    await expect(lines(page).first()).toContainText("scrub pass finished (manual, set production/postgres-primary): 2 checked, 2 passed");
    await expect(panel(page).getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
    await expect(panel(page).getByTestId("stage-count")).toHaveCount(0);
    await expect(following(page)).toHaveCount(0);
    await expect(page.getByText("retnd assurance show aop_1200_scrub", { exact: true })).toBeVisible();
    await noEmptyTerminal(page);
  });

  test("a run whose lines were pruned is said in words, with no empty strip", async ({ page }) => {
    await open(page, OPS + "aop_1200_pruned", "operation-history", { wire: true });
    const none = page.getByTestId("operation-none");
    // After a short grace period (the writer behind the record batches), then said.
    await expect(none).toBeVisible({ timeout: 15_000 });
    await expect(none).toContainText("Nothing is on record for this operation");
    await expect(none).toContainText("30 days");
    await expect(page.locator(".aop__lines")).toHaveCount(0);
    await expect(page.getByRole("progressbar")).toHaveCount(0);
    await expect(page.getByText(/^Stage \d of \d$/)).toHaveCount(0);
    await expect(following(page)).toHaveCount(0);
    await expect(page.getByText(/Could not read progress/)).toHaveCount(0);
    await expect(page.getByText("retnd assurance show aop_1200_pruned", { exact: true })).toBeVisible();
  });

  test("no finished run draws an empty terminal or offers Following", async ({ page }) => {
    for (const id of ["aop_1200_passed", "aop_1200_failed", "aop_1200_cancelled", "aop_1200_scrub", "aop_1200_pruned"]) {
      await open(page, OPS + id, "operation-history", { wire: true });
      await expect(page.getByTestId(/^operation-(progress|none)$/)).toBeVisible({ timeout: 15_000 });
      await noEmptyTerminal(page);
      await expect(following(page), id).toHaveCount(0);
    }
  });
});
