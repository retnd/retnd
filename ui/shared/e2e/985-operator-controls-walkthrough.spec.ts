import { expect, test } from "@playwright/test";
import { open } from "./helpers";

/**
 * Issue #985: one pass through the three operator controls, in the order an
 * operator meets them, with an assertion on the page at every step. It is the
 * flow the PR's GIF is cut from (E2E_VIDEO=1), and it is a real test as well,
 * since a recording of a page nobody checked proves nothing.
 *
 * Scrub now is followed to a finish, a second scrub is cancelled part way,
 * and the validator's output is opened last.
 */
test("#985 Scrub now, Cancel and the validator log, in one pass", async ({ page }) => {
  await open(page, "/sets/production/postgres-primary", "assurance-validator-output", { wire: true });
  const section = page.getByRole("region", { name: "Assurance" });
  await section.scrollIntoViewIfNeeded();

  // 1. Scrub now, followed to a finish
  await section.getByRole("button", { name: "Scrub now" }).click();
  const prog = section.getByTestId("operation-progress");
  await expect(prog).toHaveAttribute("data-state", "running");
  await expect(section.getByText(/^retnd assurance cancel aop_/)).toBeVisible();
  await expect(prog).toHaveAttribute("data-state", "complete", { timeout: 20_000 });
  await expect(prog.getByRole("status")).toHaveText("Scrub finished");
  await expect(section.locator('[data-claim="content"]')).toContainText("just now");

  // 2. A second scrub, cancelled
  await section.getByRole("button", { name: "Scrub now" }).click();
  await expect(prog).toHaveAttribute("data-state", "running");
  await section.getByRole("button", { name: "Cancel" }).click();
  await expect(section.getByTestId("operation-cancelling")).toHaveText("Cancelling");
  await expect(prog).toHaveAttribute("data-state", "cancelled", { timeout: 20_000 });
  await expect(prog.getByRole("status")).toHaveText("Scrub cancelled");

  // 3. The validator's output: inert, with the runner's marker outside it
  await section.getByRole("button", { name: "Show validator output" }).click();
  const log = page.getByTestId("validator-log");
  await expect(log).toContainText("\\x1b]0;pwned\\x07");
  await expect(page.getByTestId("truncation-marker")).toContainText("output truncated: 41873 bytes were dropped");
  await log.scrollIntoViewIfNeeded();
  await page.waitForTimeout(1200);
});
