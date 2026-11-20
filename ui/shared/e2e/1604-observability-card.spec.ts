import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { contrastReport, open, setTheme } from "./helpers";

/**
 * #1604 and #1591: the Observability card on Settings, in a real browser.
 *
 * The log level is shown once, as a labelled select whose label carries its
 * own tooltip, with the in-force line under it. Each of the five alert inputs
 * opens its three-part field help, the alert controls print the terminal line
 * for what is on screen, and the card's copy no longer says everything is off
 * until it's turned on. Contrast is measured on the computed colours in both
 * themes, for the alert block (its new command lines included) and for an
 * open field-help pop-up.
 */
const card = (page: Page) => page.locator("[data-observability]");
const alerts = (page: Page) => page.getByTestId("alert-delivery");
// Exact, because a tooltip carries a "Close help for <label>" button that a
// substring match would also find.
const control = (page: Page, label: string) => page.getByLabel(label, { exact: true });
const labelFor = async (page: Page, label: string): Promise<Locator> => {
  const id = await control(page, label).getAttribute("id");
  expect(id, label + " has no id for a <label for> to name").toBeTruthy();
  return page.locator(`label[for="${id}"]`);
};
const patch = (flags: string) => "retnd settings observability patch " + flags;

// Centres an element, clear of the terminal dock, and holds the frame a moment
// when a video is being recorded so the GIF shows the state the assertions
// judged.
async function settle(page: Page, on: Locator) {
  await on.scrollIntoViewIfNeeded();
  await on.evaluate((el) => el.scrollIntoView({ block: "center" }));
  if (process.env.E2E_VIDEO) {
    const hide = page.getByRole("button", { name: "Hide terminal" });
    if (await hide.isVisible()) await hide.click();
    await on.evaluate((el) => el.scrollIntoView({ block: "center" }));
    await page.waitForTimeout(1100);
  }
}

// Holds the frame while a video is recorded, without moving anything, so a
// pop-up that is open stays open in the GIF.
async function hold(page: Page) {
  if (process.env.E2E_VIDEO) await page.waitForTimeout(1300);
}

async function toAlertmanager(page: Page) {
  await control(page, "Alert delivery").selectOption("webhook");
  await expect(control(page, "Webhook format")).toBeVisible();
  await control(page, "Webhook format").selectOption("alertmanager");
  await expect(control(page, "Expire after (seconds)")).toBeVisible();
}

test.describe("#1604 Observability card", () => {
  test("#1604 #1591 the log level shows once, with a visible label whose tooltip explains the select", async ({ page }) => {
    await open(page, "/settings");
    await expect(control(page, "Log level")).toHaveValue("info");
    await settle(page, page.getByTestId("logs-level-control"));

    await expect(page.getByTestId("observability-summary")).toHaveCount(0);
    await expect(card(page).getByText("info (from config)", { exact: true })).toHaveCount(0);
    await expect(page.getByTestId("logs-level-in-force")).toContainText("In force: info (from config)");

    const label = await labelFor(page, "Log level");
    await expect(label).toHaveText("Log level");
    await label.getByText("Log level", { exact: true }).hover();
    const pop = page.locator(".tooltip__pop:not([hidden])");
    await expect(pop).toContainText("RETND_DEBUG");
    await expect(pop).toContainText("no restart");
    await hold(page);
  });

  test("#1604 the card's intro says logging is always on, not that everything is off", async ({ page }) => {
    await open(page, "/settings");
    const intro = card(page).locator(".card__body > p").first();
    await settle(page, intro);
    await expect(intro).not.toContainText("All of it is off");
    await expect(intro).toContainText("always");
    await hold(page);
  });

  test("#1604 each alert input opens its three-part help, and the alert block echoes the terminal line", async ({ page }) => {
    await open(page, "/settings");
    await settle(page, alerts(page));
    await expect(alerts(page)).toContainText(patch("--alerts-delivery platform"));

    await toAlertmanager(page);
    await expect(alerts(page)).toContainText(patch("--alerts-delivery webhook"));
    await expect(alerts(page)).toContainText(patch("--webhook-dialect alertmanager"));
    await expect(alerts(page)).toContainText(patch("--webhook-allow-insecure-http=false"));
    await expect(alerts(page)).toContainText(patch("--webhook-refresh-interval 4m"));
    await expect(alerts(page)).toContainText(patch("--webhook-resolve-ttl 10m"));
    await settle(page, alerts(page));

    for (const label of ["Alert delivery", "Webhook format", "Plain http", "Refresh every (seconds)", "Expire after (seconds)"]) {
      const input = control(page, label);
      await input.hover();
      const help = page.locator(".fieldhelp__pop:not([hidden])");
      await expect(help, label + " opened no field help").toHaveCount(1);
      await expect(help).toContainText("For example");
      await hold(page);
      await page.mouse.move(0, 0);
      await expect(page.locator(".fieldhelp__pop:not([hidden])")).toHaveCount(0);
    }
  });

  test("#1604 the delivery tooltip says a receiver you switch to is not told about standing alerts", async ({ page }) => {
    await open(page, "/settings");
    await settle(page, alerts(page));
    await (await labelFor(page, "Alert delivery")).hover();
    const pop = page.locator(".tooltip__pop:not([hidden])");
    await expect(pop).toContainText("per channel setting, not per receiver");
    await expect(pop).not.toContainText("keeps what has already been announced");
    await hold(page);
  });

  for (const theme of ["light", "dark"] as const) {
    test("#1604 the alert block and its field help clear WCAG AA in the " + theme + " theme", async ({ page }) => {
      await open(page, "/settings");
      await setTheme(page, theme);
      await toAlertmanager(page);
      await expect(alerts(page)).toContainText("--webhook-resolve-ttl 10m");
      await settle(page, alerts(page));

      const rows = await contrastReport(page, '[data-testid="alert-delivery"]');
      expect(rows.length).toBeGreaterThan(4);
      const echo = rows.find((r) => r.text.startsWith("retnd settings observability patch --"));
      expect(echo, "the alert command lines were not measured").toBeTruthy();
      console.log("#1604 " + theme + " alert command line " + echo!.ratio.toFixed(2) + ":1 (" + echo!.fg + " on " + echo!.bg + ")");
      const label = rows.find((r) => r.text === "Alert delivery");
      if (label) console.log("#1604 " + theme + " alert label " + label.ratio.toFixed(2) + ":1 (" + label.fg + " on " + label.bg + ")");
      for (const r of rows) expect(r.ratio, r.sel + " " + JSON.stringify(r.text)).toBeGreaterThanOrEqual(r.need);

      await control(page, "Refresh every (seconds)").hover();
      await expect(page.locator(".fieldhelp__pop:not([hidden])")).toHaveCount(1);
      const help = await contrastReport(page, ".fieldhelp__pop:not([hidden])");
      expect(help.length).toBeGreaterThan(2);
      const worst = help.reduce((a, b) => (a.ratio - a.need < b.ratio - b.need ? a : b));
      console.log("#1604 " + theme + " field help worst pair " + worst.ratio.toFixed(2) + ":1 (" + worst.fg + " on " + worst.bg + ") for " + JSON.stringify(worst.text));
      for (const r of help) expect(r.ratio, r.sel + " " + JSON.stringify(r.text)).toBeGreaterThanOrEqual(r.need);
      await hold(page);
    });
  }
});
