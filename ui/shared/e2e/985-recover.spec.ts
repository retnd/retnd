import { expect, test } from "@playwright/test";
import { open, row, contrastFailures } from "./helpers";

async function toDetail(page: import("@playwright/test").Page, file: string, scenario = "default") {
  await open(page, "/backups", scenario);
  await row(page, file).click();
}

test.describe("#985 Recover artifact", () => {
  test("offers the three targets and echoes the command, over plain http", async ({ page }) => {
    await toDetail(page, "postgres-prod-20260828.dump.zst");
    await page.getByRole("button", { name: "Recover artifact" }).click();
    const dlg = page.getByRole("dialog", { name: "Recover artifact" });
    for (const t of ["Download in this browser", "Staging path on the host", "Configured destination"]) {
      await expect(dlg.getByRole("radio", { name: t })).toBeVisible();
    }
    await expect(dlg.getByText("retnd recover", { exact: false })).toBeVisible();
    // the page is served over http://127.0.0.1 and the dialog must not need the Clipboard API
    expect(await page.evaluate(() => "clipboard" in navigator && !window.isSecureContext)).toBe(false);
    await page.evaluate(() => { Object.defineProperty(navigator, "clipboard", { value: undefined }); });
    await dlg.getByRole("radio", { name: "Staging path on the host" }).check();
    await expect(dlg.getByRole("textbox", { name: /Recover to/ })).toBeVisible();
  });

  test("an archived copy blocks on a cost decision rather than a dismissible notice", async ({ page }) => {
    await toDetail(page, "media-week34.tar");
    await page.getByRole("button", { name: "Recover artifact" }).click();
    const dlg = page.getByRole("dialog", { name: "Recover artifact" });
    const decision = dlg.getByRole("group", { name: "This copy is archived" });
    await expect(decision).toContainText(/costs money and takes time/i);
    const pay = dlg.getByRole("button", { name: "Pay for the thaw and recover" });
    await expect(pay).toBeDisabled();
    await page.keyboard.press("Enter");
    await expect(dlg.getByTestId("download-progress")).toHaveCount(0);
    await dlg.getByRole("checkbox", { name: "I accept this cost" }).check();
    await expect(pay).toBeEnabled();
    // Escape means "do not recover", and nothing was started
    await page.keyboard.press("Escape");
    await expect(dlg).toHaveCount(0);
    expect(await page.evaluate(() => sessionStorage.getItem("retnd.mock.assurance"))).not.toContain('"grants":1');
  });

  test("a cancelled download and a failed download are their own outcomes, never a success", async ({ page }) => {
    await toDetail(page, "postgres-prod-20260828.dump.zst", "assurance-download-slow");
    await page.getByRole("button", { name: "Recover artifact" }).click();
    const dlg = page.getByRole("dialog", { name: "Recover artifact" });
    await dlg.getByRole("button", { name: "Recover artifact" }).click();
    const prog = dlg.getByTestId("download-progress");
    await expect(prog).toBeVisible();
    await expect(prog).toHaveAttribute("data-state", "running");
    await dlg.getByRole("button", { name: "Cancel download" }).click();
    await expect(prog).toHaveAttribute("data-state", "cancelled");
    await expect(dlg).toContainText("Cancelled: nothing saved");
    await expect(dlg.getByRole("link", { name: /Save|Download complete/ })).toHaveCount(0);
    await expect(dlg.getByText(/Recovered|Download complete/)).toHaveCount(0);

    await toDetail(page, "postgres-prod-20260828.dump.zst", "assurance-download-fails");
    await page.getByRole("button", { name: "Recover artifact" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Recover artifact" }).click();
    const p2 = page.getByTestId("download-progress");
    await expect(p2).toHaveAttribute("data-state", "failed", { timeout: 15_000 });
    await expect(page.getByRole("dialog")).toContainText("Failed: nothing saved");
    await expect(page.getByRole("dialog").getByText(/Recovered|Download complete/)).toHaveCount(0);
  });

  test("a download that completes is the only path to a success rendering", async ({ page }) => {
    await toDetail(page, "postgres-prod-20260828.dump.zst");
    await page.getByRole("button", { name: "Recover artifact" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Recover artifact" }).click();
    await expect(page.getByTestId("download-progress")).toHaveAttribute("data-state", "complete", { timeout: 20_000 });
    await expect(page.getByRole("dialog")).toContainText("Recovered");
  });
});

test.describe("#985 recovery continuity and keyboard", () => {
  test("a reload mid-download re-attaches to the running recovery and starts no second one", async ({ page }) => {
    // Opened by its own URL, so a reload keeps the scenario the way a real
    // deployment keeps its state.
    await open(page, "/backups/production/postgres-primary/postgres-prod-20260828.dump.zst", "assurance-download-slow");
    await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Recover artifact", exact: true }).click();
    const prog = page.getByTestId("download-progress");
    await expect(prog).toHaveAttribute("data-state", "running");
    await page.waitForTimeout(1500);
    const before = Number(await prog.getAttribute("data-bytes"));
    expect(before).toBeGreaterThan(0);

    await page.reload();
    const after = page.getByRole("dialog").getByTestId("download-progress");
    await expect(after).toHaveAttribute("data-state", "running");
    expect(Number(await after.getAttribute("data-bytes"))).toBeGreaterThanOrEqual(before);
    const state = JSON.parse((await page.evaluate(() => sessionStorage.getItem("retnd.mock.assurance"))) ?? "{}");
    expect(state.grants).toBe(1);
  });

  test("the dialog is operable by keyboard, holds focus, and gives it back on Escape", async ({ page }) => {
    await toDetail(page, "postgres-prod-20260828.dump.zst");
    const opener = page.getByRole("button", { name: "Recover artifact", exact: true });
    await opener.focus();
    await page.keyboard.press("Enter");
    const dlg = page.getByRole("dialog", { name: "Recover artifact" });
    await expect(dlg).toBeVisible();
    for (let i = 0; i < 12; i++) {
      await page.keyboard.press("Tab");
      expect(await page.evaluate(() => !!document.activeElement?.closest('[role="dialog"]'))).toBe(true);
    }
    await page.keyboard.press("Escape");
    await expect(dlg).toHaveCount(0);
    await expect(opener).toBeFocused();
  });

  test("the recovery surfaces fit a narrow screen without sideways scrolling", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 800 });
    await toDetail(page, "media-week34.tar");
    await page.getByRole("button", { name: "Recover artifact" }).click();
    const dlg = page.getByRole("dialog", { name: "Recover artifact" });
    await expect(dlg.getByRole("group", { name: "This copy is archived" })).toBeVisible();
    const box = await dlg.boundingBox();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(390);
  });
});

for (const theme of ["light", "dark"] as const) {
  test(`#985 the dialog, the operation panel and the validator log meet contrast in the ${theme} theme`, async ({ page }) => {
    await toDetail(page, "media-week34.tar");
    await page.evaluate((t) => document.documentElement.setAttribute("data-theme", t), theme);
    await page.getByRole("button", { name: "Recover artifact" }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    expect(await contrastFailures(page, '[role="dialog"]')).toEqual([]);
    await page.keyboard.press("Escape");

    await open(page, "/sets/production/postgres-primary", "assurance-validator-output");
    await page.evaluate((t) => document.documentElement.setAttribute("data-theme", t), theme);
    const section = page.getByRole("region", { name: "Assurance" });
    await section.getByRole("button", { name: "Run recovery test" }).click();
    await expect(section.getByTestId("operation-progress")).toBeVisible();
    await section.getByRole("button", { name: "Show validator output" }).click();
    await expect(page.getByTestId("validator-log")).toBeVisible();
    expect(await contrastFailures(page, '[aria-label="Assurance"]')).toEqual([]);
  });
}
