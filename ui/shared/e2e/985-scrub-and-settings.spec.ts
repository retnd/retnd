import { expect, test } from "@playwright/test";
import { open, measureContrast, setTheme, spaGo } from "./helpers";

const SET = "/sets/production/postgres-primary";
const region = (page: import("@playwright/test").Page) => page.getByRole("region", { name: "Assurance" });

test.describe("#985 Scrub now on a backup set", () => {
  test("a scrub that fails replaces the Content verified chip instead of leaving it", async ({ page }) => {
    await open(page, SET, "assurance-scrub-fails");
    const section = region(page);
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
    const section = region(page);
    await section.getByRole("button", { name: "Scrub now" }).click();
    await expect(section.getByTestId("operation-progress")).toBeVisible();
    await expect(section.getByRole("button", { name: "Scrub now" })).toBeDisabled();
    // the previous answer is still on screen, so it is drawn as one being re-checked
    await expect(section.locator('[data-claim="content"]')).toHaveAttribute("data-checking", "true");
  });

  test("the terminal command printed is one the CLI has, and no recovery-test verb is invented", async ({ page }) => {
    await open(page, SET);
    const section = region(page);
    await expect(section.getByText("retnd scrub production/postgres-primary", { exact: true })).toBeVisible();
    await expect(section.getByText("retnd recovery-test", { exact: false })).toHaveCount(0);
  });

  test("a server with no route to start a run offers the command and does not fake a run", async ({ page }) => {
    await open(page, SET, "assurance-no-operations");
    const section = region(page);
    await expect(section.getByTestId("no-run-route")).toContainText(/no route to start a scrub or a recovery test/i);
    await expect(section.getByRole("button", { name: "Scrub now" })).toHaveCount(0);
    await expect(section.getByRole("button", { name: "Run recovery test" })).toHaveCount(0);
    await expect(section.getByRole("button", { name: "Show validator output" })).toHaveCount(0);
    await expect(section.getByText("retnd scrub production/postgres-primary", { exact: true })).toBeVisible();
    await expect(section.getByText("retnd scrub production/postgres-primary --content --allow-egress", { exact: true })).toBeVisible();
    // and the claims that do not need the missing route are still on screen
    await expect(section.locator('[data-claim="content"]')).toBeVisible();
  });

  test("the per-set section offers follow-the-deployment or an own policy, and says what following resolves to", async ({ page }) => {
    await open(page, SET);
    const group = region(page).getByRole("radiogroup", { name: "Assurance policy for this set" });
    await expect(group.getByRole("radio", { name: "Follow the deployment" })).toBeChecked();
    await expect(group.getByRole("radio", { name: "This set's own policy" })).not.toBeChecked();
    await expect(region(page).getByTestId("resolved-policy")).toContainText(/Following the deployment: scrub every 7d, reading auto\. Recovery tests are off\./);
  });

  test("an own policy pins values and the server keeps them when the deployment changes", async ({ page }) => {
    await open(page, SET);
    const section = region(page);
    await section.getByRole("radio", { name: "This set's own policy" }).check();
    await section.getByLabel("Scrub every").fill("12h");
    await section.getByRole("button", { name: "Save policy" }).click();
    await expect(section.getByText("Saved")).toBeVisible();
    // the deployment moves; the set that pinned its own cadence does not
    await spaGo(page, "/settings?scenario=default");
    const card = region(page);
    await card.getByLabel("Scrub every").fill("3d");
    await card.getByRole("button", { name: "Save" }).click();
    await expect(card.getByText("Saved")).toBeVisible();
    await spaGo(page, SET + "?scenario=default");
    await expect(region(page).getByLabel("Scrub every")).toHaveValue("12h");
  });

  test("a cadence under an hour and a required test with no validator are refused before saving", async ({ page }) => {
    await open(page, SET);
    const section = region(page);
    await section.getByRole("radio", { name: "This set's own policy" }).check();
    await section.getByLabel("Scrub every").fill("30m");
    await expect(section.getByRole("alert")).toContainText("The shortest cadence is 1h.");
    await expect(section.getByRole("button", { name: "Save policy" })).toBeDisabled();
    await section.getByLabel("Scrub every").fill("1d");
    await section.getByLabel("Recovery test", { exact: true }).selectOption("required");
    await expect(section.getByRole("alert")).toContainText(/needs a validator/);
    await expect(section.getByRole("button", { name: "Save policy" })).toBeDisabled();
    await section.getByLabel("Validator").fill("pg-restore-check");
    await expect(section.getByRole("button", { name: "Save policy" })).toBeEnabled();
  });

  test("the validator is described as a script the engine runs, and the page never lets a script claim a result", async ({ page }) => {
    await open(page, SET);
    const section = region(page);
    await section.getByRole("radio", { name: "This set's own policy" }).check();
    await section.getByLabel("Recovery test", { exact: true }).selectOption("required");
    await expect(section).toContainText("NAME.local.sh");
    await expect(section).toContainText("recovery-test/production/postgres-primary");
    await expect(section).toContainText(/engine re-reads the recovered copy and checks its digest, then runs the script and records the claim itself/);
  });

  test("the host-scoped knobs have no per-set control, even under an own policy", async ({ page }) => {
    await open(page, SET);
    const section = region(page);
    await section.getByRole("radio", { name: "This set's own policy" }).check();
    for (const knob of ["staging root", "staging ttl", "concurren", "memory budget"]) {
      await expect(section.getByLabel(new RegExp(knob, "i"))).toHaveCount(0);
    }
    await expect(section.getByText(/belong to the host/i)).toBeVisible();
  });
});

test.describe("#985 the global Assurance card on Settings", () => {
  test("states the smart default, the first-run state, and shows the host knobs as deployment-wide readouts with their reason", async ({ page }) => {
    await open(page, "/settings");
    const card = region(page);
    await expect(card).toContainText(/never thaws an archive/i);
    await expect(card.getByTestId("assurance-first-run")).toContainText(/nothing needs setting up first/i);
    const knobs = card.locator('[data-testid="host-knob"]');
    await expect(knobs).toHaveCount(5);
    for (const k of await knobs.all()) {
      await expect(k).toHaveAttribute("data-scope", "deployment-wide");
      await expect(k.locator("input, select, textarea, button")).toHaveCount(0);
      await expect(k).toContainText("Deployment-wide");
      await expect(k).toContainText(/per-set (value|override) could not be (honoured|enforced)/i);
    }
  });

  test("the validator is explained as an engine-run script and a required test without one is refused", async ({ page }) => {
    await open(page, "/settings");
    const card = region(page);
    await expect(card).toContainText(/never writes Content verified or Recovery tested/);
    await card.getByLabel("Recovery test", { exact: true }).selectOption("required");
    await expect(card.getByRole("alert")).toContainText(/needs a validator/);
    await expect(card.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  test("a global change moves a set that follows the deployment, through the page", async ({ page }) => {
    await open(page, "/settings");
    const card = region(page);
    const cadence = card.getByLabel("Scrub every");
    await cadence.fill("3d");
    await card.getByRole("button", { name: "Save" }).click();
    await expect(card.getByText("Saved")).toBeVisible();
    await spaGo(page, SET + "?scenario=default");
    await expect(region(page).getByTestId("resolved-policy")).toContainText("scrub every 3d");
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`#985 the Assurance cards meet contrast in the ${theme} theme`, async ({ page }) => {
    await open(page, "/settings");
    await expect(region(page)).toBeVisible();
    await setTheme(page, theme);
    expect(await measureContrast(page, `settings card, ${theme}`, '[aria-label="Assurance"]')).toEqual([]);
    await spaGo(page, SET + "?scenario=default");
    await expect(region(page)).toBeVisible();
    await setTheme(page, theme);
    await region(page).getByRole("radio", { name: "This set's own policy" }).check();
    expect(await measureContrast(page, `set section, ${theme}`, '[aria-label="Assurance"]')).toEqual([]);
  });
}
