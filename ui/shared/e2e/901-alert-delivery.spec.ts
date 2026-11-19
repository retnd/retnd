import { expect, test } from "@playwright/test";
import { contrastReport, open, setTheme } from "./helpers";

/**
 * EPIC O, #901 (FR-101): the alert delivery controls in Settings, in a real
 * browser. Delivery selects one channel; the webhook URL is never a field and
 * is never shown; a URL the engine cannot read is stated loudly and is not
 * mistaken for a quiet success. Contrast is measured on the computed colours
 * in both themes.
 */
const card = (page: import("@playwright/test").Page) => page.getByTestId("alert-delivery");

test.describe("#901 alert delivery controls", () => {
  test("#901 delivery, format, plain http and the alertmanager timers appear as the choice narrows", async ({ page }) => {
    await open(page, "/settings");
    await expect(page.getByLabel("Alert delivery")).toHaveValue("platform");
    await expect(page.getByLabel("Webhook format")).toHaveCount(0);

    await page.getByLabel("Alert delivery").selectOption("webhook");
    await expect(page.getByLabel("Webhook format")).toBeVisible();
    await expect(page.getByLabel("Plain http")).toBeVisible();
    await expect(page.getByLabel("Refresh every (seconds)")).toHaveCount(0);

    await page.getByLabel("Webhook format").selectOption("alertmanager");
    await expect(page.getByLabel("Refresh every (seconds)")).toBeVisible();
    await expect(page.getByLabel("Expire after (seconds)")).toBeVisible();
    // The URL is not an input anywhere in the card.
    await expect(card(page).getByRole("textbox")).toHaveCount(0);
  });

  test("#901 a URL the engine cannot read is stated as an alert", async ({ page }) => {
    await open(page, "/settings", "default");
    await page.goto("/settings?scenario=default&webhookurl=unresolvable");
    await page.getByLabel("Alert delivery").selectOption("webhook");
    const alert = page.getByTestId("alert-webhook-unresolvable");
    await expect(alert).toBeVisible();
    await expect(alert).toContainText("cannot be read");
  });

  for (const theme of ["light", "dark"] as const) {
    test("#901 text in the card clears WCAG AA in the " + theme + " theme, including the unreadable-URL alert", async ({ page }) => {
      await open(page, "/settings", "default");
      await page.goto("/settings?scenario=default&webhookurl=unresolvable");
      await setTheme(page, theme);
      await page.getByLabel("Alert delivery").selectOption("webhook");
      await page.getByLabel("Webhook format").selectOption("alertmanager");
      await expect(page.getByTestId("alert-webhook-unresolvable")).toBeVisible();
      const rows = await contrastReport(page, '[data-testid="alert-delivery"]');
      expect(rows.length).toBeGreaterThan(3);
      const worst = rows.reduce((a, b) => (a.ratio - a.need < b.ratio - b.need ? a : b));
      console.log("#901 " + theme + " worst text pair " + worst.ratio.toFixed(2) + ":1 (needs " + worst.need + ") for " + JSON.stringify(worst.text.slice(0, 40)));
      for (const r of rows) expect(r.ratio, r.sel + " " + JSON.stringify(r.text)).toBeGreaterThanOrEqual(r.need);
    });
  }
});
