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
// Exact, because each control's tooltip carries a "Close help for <label>"
// button that a substring match would also find.
const field = (page: import("@playwright/test").Page, label: string) => page.getByLabel(label, { exact: true });
// Centres the card's alert (or the card) in the view, clear of the terminal
// dock, and holds the frame a moment when a video is being recorded, so the
// recording shows the state the assertions judged.
async function settle(page: import("@playwright/test").Page) {
  await card(page).evaluate((el) => (el.querySelector('[role="alert"]') ?? el).scrollIntoView({ block: "center" }));
  if (process.env.E2E_VIDEO) {
    // The terminal dock covers the lower half of the window; folding it
    // changes nothing in the card, and lets the recording show the card.
    const hide = page.getByRole("button", { name: "Hide terminal" });
    if (await hide.isVisible()) await hide.click();
    await card(page).evaluate((el) => (el.querySelector('[role="alert"]') ?? el).scrollIntoView({ block: "center" }));
    await page.waitForTimeout(900);
  }
}

test.describe("#901 alert delivery controls", () => {
  test("#901 delivery, format, plain http and the alertmanager timers appear as the choice narrows", async ({ page }) => {
    await open(page, "/settings");
    await settle(page);
    await expect(field(page, "Alert delivery")).toHaveValue("platform");
    await expect(field(page, "Webhook format")).toHaveCount(0);

    await field(page, "Alert delivery").selectOption("webhook");
    await expect(field(page, "Webhook format")).toBeVisible();
    await expect(field(page, "Plain http")).toBeVisible();
    await expect(field(page, "Refresh every (seconds)")).toHaveCount(0);

    await settle(page);
    await field(page, "Webhook format").selectOption("alertmanager");
    await expect(field(page, "Refresh every (seconds)")).toBeVisible();
    await expect(field(page, "Expire after (seconds)")).toBeVisible();
    // The URL is not an input anywhere in the card.
    await expect(card(page).getByRole("textbox")).toHaveCount(0);
    await settle(page);
  });

  test("#901 a URL the engine cannot read is stated as an alert", async ({ page }) => {
    await open(page, "/settings", "default");
    await page.goto("/settings?scenario=default&webhookurl=unresolvable");
    await field(page, "Alert delivery").selectOption("webhook");
    const alert = page.getByTestId("alert-webhook-unresolvable");
    await expect(alert).toBeVisible();
    await expect(alert).toContainText("cannot be read");
    await settle(page);
  });

  test("#901 a URL that reads and is not acceptable is stated as an alert of its own", async ({ page }) => {
    await open(page, "/settings", "default");
    await page.goto("/settings?scenario=default&webhookurl=invalid");
    await field(page, "Alert delivery").selectOption("webhook");
    const alert = page.getByTestId("alert-webhook-invalid");
    await expect(alert).toBeVisible();
    await expect(alert).toContainText("not an acceptable URL");
    await expect(page.getByTestId("alert-webhook-unresolvable")).toHaveCount(0);
    await settle(page);
  });

  for (const theme of ["light", "dark"] as const) {
    test("#901 text in the card clears WCAG AA in the " + theme + " theme, including the unreadable-URL alert", async ({ page }) => {
      await open(page, "/settings", "default");
      await page.goto("/settings?scenario=default&webhookurl=unresolvable");
      await setTheme(page, theme);
      await field(page, "Alert delivery").selectOption("webhook");
      await field(page, "Webhook format").selectOption("alertmanager");
      await expect(page.getByTestId("alert-webhook-unresolvable")).toBeVisible();
      await settle(page);
      const rows = await contrastReport(page, '[data-testid="alert-delivery"]');
      expect(rows.length).toBeGreaterThan(3);
      const worst = rows.reduce((a, b) => (a.ratio - a.need < b.ratio - b.need ? a : b));
      console.log("#901 " + theme + " worst text pair " + worst.ratio.toFixed(2) + ":1 (needs " + worst.need + ") for " + JSON.stringify(worst.text.slice(0, 40)));
      const loud = rows.find((r) => r.text.startsWith("The webhook URL cannot be read"));
      expect(loud, "the unreadable-URL alert was not measured").toBeTruthy();
      console.log("#901 " + theme + " unreadable-URL alert " + loud!.ratio.toFixed(2) + ":1 (" + loud!.fg + " on " + loud!.bg + ")");
      for (const r of rows) expect(r.ratio, r.sel + " " + JSON.stringify(r.text)).toBeGreaterThanOrEqual(r.need);
    });
  }
});
