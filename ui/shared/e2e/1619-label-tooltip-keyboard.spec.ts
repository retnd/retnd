import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * #1619: a tooltip on a control's label reaches the control. Tabbing to the
 * Observability card's inputs and to the Suite group opens the registry
 * tooltip with no pointer, the control's own aria-describedby names that
 * tooltip's copy (not only the label's span), Escape puts it away, and a
 * touch tap on the label keeps it up.
 */
const control = (page: Page, label: string) => page.getByLabel(label, { exact: true });

// Real Tab presses from the top of the page until the element has focus, so
// the spec proves the control is reachable by keyboard and not only focusable.
async function tabTo(page: Page, el: Locator) {
  await el.scrollIntoViewIfNeeded();
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  for (let i = 0; i < 200; i++) {
    await page.keyboard.press("Tab");
    if (await el.evaluate((n) => n === document.activeElement)) return;
  }
  throw new Error("Tab never reached the control");
}

/** The registry pop-up the control's aria-describedby points at, or null. */
async function registryDescription(el: Locator) {
  return el.evaluate((n) => {
    const ids = (n.getAttribute("aria-describedby") ?? "").split(/\s+/).filter(Boolean);
    for (const id of ids) {
      const node = document.getElementById(id);
      if (node?.classList.contains("tooltip__body")) return node.textContent ?? "";
    }
    return null;
  });
}

async function hold(page: Page) {
  if (process.env.E2E_VIDEO) await page.waitForTimeout(1300);
}

const popUp = (page: Page) => page.locator(".tooltip__pop:not([hidden])");

async function toAlertmanager(page: Page) {
  await control(page, "Alert delivery").selectOption("webhook");
  await control(page, "Webhook format").selectOption("alertmanager");
  await expect(control(page, "Expire after (seconds)")).toBeVisible();
}

const CONTROLS = ["Log level", "Alert delivery", "Webhook format", "Plain http", "Refresh every (seconds)", "Expire after (seconds)", "Report cache (seconds)"];

test.describe("#1619 label tooltips reach the control", () => {
  for (const label of CONTROLS) {
    test(`#1619 tabbing to "${label}" opens its tooltip and the control is described by it`, async ({ page }) => {
      await open(page, "/settings");
      if (label === "Report cache (seconds)") await control(page, "Serve the scrape endpoint").check();
      else if (label !== "Log level" && label !== "Alert delivery") await toAlertmanager(page);
      const el = control(page, label);
      await expect(el).toBeVisible();

      expect(await registryDescription(el), label + " is not described by its registry tooltip").toBeTruthy();

      await tabTo(page, el);
      await expect(popUp(page)).toHaveCount(1);
      await hold(page);
      await page.keyboard.press("Escape");
      await expect(popUp(page)).toHaveCount(0);
    });
  }

  test("#1619 the serve-the-scrape-endpoint checkbox is described by its tooltip and opens it on Tab", async ({ page }) => {
    await open(page, "/settings");
    const box = control(page, "Serve the scrape endpoint");
    expect(await registryDescription(box)).toBeTruthy();
    await tabTo(page, box);
    await expect(popUp(page)).toHaveCount(1);
  });

  test("#1619 the Suite group is described by its tooltip and Tab into it opens it", async ({ page }) => {
    await open(page, "/settings", "encryption-suite-change");
    const group = page.getByRole("radiogroup", { name: "Suite" });
    await expect(group).toBeVisible();
    expect(await registryDescription(group)).toBeTruthy();
    await tabTo(page, group.getByRole("radio").first());
    await expect(popUp(page)).toHaveCount(1);
    await hold(page);
  });

  test("#1619 moving focus on closes the tooltip", async ({ page }) => {
    await open(page, "/settings");
    await tabTo(page, control(page, "Log level"));
    await expect(popUp(page)).toHaveCount(1);
    await page.keyboard.press("Tab");
    await page.keyboard.press("Tab");
    await expect(popUp(page)).toHaveCount(0);
  });

  for (const theme of ["light", "dark"] as const) {
    test(`#1619 the focus-opened tooltip meets WCAG AA in ${theme}`, async ({ page }) => {
      await open(page, "/settings");
      await setTheme(page, theme);
      await tabTo(page, control(page, "Log level"));
      await expect(popUp(page)).toHaveCount(1);
      const failures = await measureContrast(page, "#1619 " + theme + " focus-opened tooltip", ".tooltip__pop:not([hidden])");
      expect(failures).toEqual([]);
    });
  }
});

test.describe("#1619 touch", () => {
  test.use({ hasTouch: true });
  test("#1619 a tap on the label opens the tooltip and it stays up", async ({ page }) => {
    await open(page, "/settings");
    const label = page.locator("label", { hasText: "Log level" }).first();
    await label.scrollIntoViewIfNeeded();
    await label.tap();
    await expect(popUp(page)).toHaveCount(1);
    await hold(page);
    await page.waitForTimeout(400);
    await expect(popUp(page)).toHaveCount(1);
  });
});
