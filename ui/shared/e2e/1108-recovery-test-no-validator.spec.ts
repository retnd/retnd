import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

/**
 * Issue #1108: Run recovery test was offered, enabled, on a set that declares
 * no validator, so the first thing an operator got was a refusal. Driven the
 * way #985's specs are: `wire=1`, so realAssurance (the code a built app runs)
 * reads the mock's routes.
 *
 * The browser half of the other two defects (the stage line and the closing
 * line the terminal prints) needs a real engine to press the button on a set
 * with no validator, so those specs live in retnd-tests
 * (suites/web-ui/e2e/recovery-test-no-validator.1108.spec.ts); the engine's
 * words are held by the Go tests beside this branch.
 */
const SET = "/sets/production/postgres-primary";
const region = (page: Page) => page.getByRole("region", { name: "Assurance" });

test.describe("#1108 Run recovery test on a set with no validator", () => {
  test("is disabled before it is pressed, with the reason beside it and a pointer to how to declare one", async ({ page }) => {
    await open(page, SET, "assurance-no-validator", { wire: true });
    const section = region(page);
    const run = section.getByRole("button", { name: "Run recovery test" });
    await expect(run).toBeVisible();
    await expect(run, "a button that can only refuse must not be offered").toBeDisabled();
    await expect(section.getByText(/no recovery-test validator is declared/i)).toBeVisible();
    await expect(section.getByRole("link", { name: /recovery/i }).first()).toHaveAttribute("href", /docs\/recovery/);
    // the run never starts: no terminal line for it
    await expect(page.getByLabel("Terminal", { exact: true }).getByRole("log")).not.toContainText("recovery test started");
  });

  test("control: the same page on a set that declares its validator offers the button, enabled", async ({ page }) => {
    await open(page, SET, "default", { wire: true });
    const run = region(page).getByRole("button", { name: "Run recovery test" });
    await expect(run).toBeEnabled();
    await expect(region(page).getByText(/no recovery-test validator is declared/i)).toHaveCount(0);
  });
});
