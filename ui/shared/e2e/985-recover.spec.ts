import { expect, test } from "@playwright/test";
import { open, row } from "./helpers";

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
