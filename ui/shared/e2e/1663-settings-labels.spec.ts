import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { open } from "./helpers";

// Issue #1663: eight inputs on /settings are not named in the way the retnd-tests
// sweep accepts. Each already has a name through a visible <label htmlFor>, but the
// sweep ("every control is reachable and labelled", accessibility.spec.ts, at the
// pin) only takes an aria-label, a title, the control's own text or a wrapping
// <label>, so it reports them. Six show on a default deployment; the other two
// (refresh and expire seconds) only show with Alertmanager webhook alerts on.
// These specs run the same sweep and then pin each input's own aria-label.

// The sweep from retnd-tests, kept in step with it on purpose: this is the
// check the release gate runs against the real deployment.
async function unlabelled(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const controls = Array.from(document.querySelectorAll("button, a[href], input, select, textarea"));
    return controls
      .filter((el) => {
        const style = getComputedStyle(el);
        if (style.display === "none" || style.visibility === "hidden") return false;
        const label = el.getAttribute("aria-label") ?? el.getAttribute("title") ?? (el as HTMLElement).innerText ?? "";
        if (label.trim().length > 0) return false;
        return !el.closest("label");
      })
      .map((el) => el.tagName + (el.className ? "." + String(el.className).slice(0, 40) : "") + (el.id ? "#" + el.id : ""));
  });
}

// Alertmanager webhook alerts on, which is what shows the two timer inputs.
async function alertmanagerOn(page: Page) {
  await page.locator("#obs-alert-delivery").selectOption("webhook");
  await page.locator("#obs-alert-dialect").selectOption("alertmanager");
  await expect(page.locator("#obs-alert-ttl")).toBeVisible();
}

async function show(page: Page, control: Locator) {
  await control.scrollIntoViewIfNeeded();
  await expect(control).toBeVisible();
  await page.waitForTimeout(700);
}

test.describe("#1663 settings inputs are labelled", () => {
  test.beforeEach(async ({ page }) => {
    await open(page, "/settings");
    await expect(page.getByRole("heading", { level: 1 })).toHaveCount(1);
    await expect(page.locator("#workflow-env-name")).toBeVisible();
  });

  test("the retnd-tests sweep finds no unlabelled control on /settings", async ({ page }) => {
    await alertmanagerOn(page);
    const found = await unlabelled(page);
    expect(found, "unlabelled controls on /settings").toEqual([]);
  });

  const named: Array<[string, (page: Page) => Locator, string, "textbox" | "spinbutton"?]> = [
    ["the Report cache interval", (p) => p.getByLabel("Report cache (seconds)", { exact: true }), "Report cache (seconds)"],
    ["the global before directory", (p) => p.locator("#workflow-setting-beforeDir"), "Global before directory"],
    ["the global after directory", (p) => p.locator("#workflow-setting-afterDir"), "Global after directory"],
    ["the default script timeout", (p) => p.locator("#workflow-setting-scriptTimeoutSeconds"), "Default script timeout (seconds)"],
    ["the workflow variable name", (p) => p.locator("#workflow-env-name"), "Name"],
    ["the alert refresh interval", (p) => p.locator("#obs-alert-refresh"), "Refresh every (seconds)", "spinbutton"],
    ["the alert expiry", (p) => p.locator("#obs-alert-ttl"), "Expire after (seconds)", "spinbutton"]
  ];
  for (const [what, find, name, role = "textbox"] of named) {
    test(`${what} is named "${name}" without a for association`, async ({ page }) => {
      await alertmanagerOn(page);
      const control = find(page);
      await show(page, control);
      await expect(control).toHaveAttribute("aria-label", name);
      await expect(control.and(page.getByRole(role, { name, exact: true }))).toHaveCount(1);
    });
  }

  for (const [kind, label] of [["literal", "Value"], ["command", "Command"], ["file", "Location"]] as const) {
    test(`the workflow variable's value input is named "${label}" when the source is ${kind}`, async ({ page }) => {
      await page.locator("#workflow-env-kind").selectOption(kind);
      const control = page.locator("#workflow-env-value");
      await show(page, control);
      await expect(control).toHaveAttribute("aria-label", label);
      await expect(control.and(page.getByRole("textbox", { name: label, exact: true }))).toHaveCount(1);
    });
  }
});
