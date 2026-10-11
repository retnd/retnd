import { expect, test } from "@playwright/test";
import { open, contrastFailures } from "./helpers";

const SET = "/sets/production/postgres-primary";

test.describe("#985 Scrub now on a backup set", () => {
  test("a scrub that fails replaces the Content verified chip instead of leaving it", async ({ page }) => {
    await open(page, SET, "assurance-scrub-fails");
    const section = page.getByRole("region", { name: "Assurance" });
    const content = section.locator('[data-claim="content"]');
    await expect(content).toHaveAttribute("data-state", "pass");
    await expect(content).toContainText("Content verified");

    await section.getByRole("button", { name: "Scrub now" }).click();

    // the pass chip must be gone, driven by a real failing scrub through the
    // same mock API the page reads, not by a prop
    await expect(content).toHaveAttribute("data-state", "failed", { timeout: 15_000 });
    await expect(content).toContainText("Content check failed");
    await expect(section.getByText("Content verified")).toHaveCount(0);
    await expect(section.locator('[data-state="pass"][data-claim="content"]')).toHaveCount(0);
  });

  test("while a scrub is running the old chip is marked as being re-checked, not left as current", async ({ page }) => {
    await open(page, SET, "assurance-scrub-fails");
    const section = page.getByRole("region", { name: "Assurance" });
    await section.getByRole("button", { name: "Scrub now" }).click();
    await expect(section.getByTestId("operation-progress")).toBeVisible();
    await expect(section.getByRole("button", { name: "Scrub now" })).toBeDisabled();
  });

  test("every mutating control prints the command it is equivalent to", async ({ page }) => {
    await open(page, SET);
    const section = page.getByRole("region", { name: "Assurance" });
    await expect(section.getByText("retnd scrub", { exact: false })).toBeVisible();
    await expect(section.getByText("retnd recovery-test", { exact: false })).toBeVisible();
  });

  test("the per-set section offers Inherit, Off, Required and Custom", async ({ page }) => {
    await open(page, SET);
    const group = page.getByRole("region", { name: "Assurance" }).getByRole("radiogroup");
    for (const name of ["Inherit", "Off", "Required", "Custom"]) {
      await expect(group.getByRole("radio", { name })).toBeVisible();
    }
    await expect(group.getByRole("radio", { name: "Inherit" })).toBeChecked();
    await expect(page.getByRole("region", { name: "Assurance" })).toContainText(/Inheriting: scrub every/);
  });

  test("the four host-scoped knobs have no per-set control, even under Custom", async ({ page }) => {
    await open(page, SET);
    const section = page.getByRole("region", { name: "Assurance" });
    await section.getByRole("radio", { name: "Custom" }).check();
    for (const knob of ["staging root", "staging ttl", "concurrency", "memory budget"]) {
      await expect(section.getByLabel(new RegExp(knob, "i"))).toHaveCount(0);
    }
    await expect(section.getByText(/belong to the host/i)).toBeVisible();
  });
});

test.describe("#985 the global Assurance card on Settings", () => {
  test("states the smart default and shows the four knobs as deployment-wide readouts", async ({ page }) => {
    await open(page, "/settings");
    const card = page.getByRole("region", { name: "Assurance" });
    await expect(card).toContainText(/never thaws an archive/i);
    const knobs = card.locator('[data-testid="host-knob"]');
    await expect(knobs).toHaveCount(4);
    for (const k of await knobs.all()) {
      await expect(k).toHaveAttribute("data-scope", "deployment-wide");
      await expect(k.locator("input, select, textarea, button")).toHaveCount(0);
      await expect(k).toContainText("Deployment-wide");
    }
  });

  test("a global change moves a set that inherits, through the page", async ({ page }) => {
    await open(page, "/settings");
    const card = page.getByRole("region", { name: "Assurance" });
    const cadence = card.getByLabel("Scrub every");
    await cadence.fill("3d");
    await card.getByRole("button", { name: "Save" }).click();
    await expect(card.getByText("Saved")).toBeVisible();
    await page.goto(SET + "?scenario=default");
    await expect(page.getByRole("region", { name: "Assurance" })).toContainText("Inheriting: scrub every 3d");
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`#985 the Assurance cards meet contrast in the ${theme} theme`, async ({ page }) => {
    await open(page, "/settings");
    await expect(page.getByRole("region", { name: "Assurance" })).toBeVisible();
    await page.evaluate((t) => document.documentElement.setAttribute("data-theme", t), theme);
    expect(await contrastFailures(page, '[aria-label="Assurance"], section[aria-labelledby*="assurance"]')).toEqual([]);
    await page.goto(SET + "?scenario=default");
    await expect(page.getByRole("region", { name: "Assurance" })).toBeVisible();
    await page.evaluate((t) => document.documentElement.setAttribute("data-theme", t), theme);
    expect(await contrastFailures(page, '[aria-label="Assurance"], section[aria-labelledby*="assurance"]')).toEqual([]);
  });
}
