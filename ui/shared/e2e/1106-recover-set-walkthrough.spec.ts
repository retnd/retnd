import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { open, setTheme } from "./helpers";

/**
 * Issue #1106: one pass through Recover this backup set, in the order an
 * operator meets it, with an assertion on the page at every step. It is the
 * flow the PR's GIF is cut from: with E2E_FRAMES=<dir> set it writes a numbered
 * PNG at each beat and a few while the run is going, and the frames are
 * assembled into the GIF afterwards. It is a real test as well, since a
 * recording of a page nobody checked proves nothing.
 *
 * The pre-flight and its quarantined row, the destination, a run followed to
 * its end, and the same set again with Cancel pressed part way (slow host).
 */
let n = 0;
async function frame(page: import("@playwright/test").Page, label: string) {
  const dir = process.env.E2E_FRAMES;
  if (!dir) return;
  mkdirSync(dir, { recursive: true });
  await page.screenshot({ path: `${dir}/${String(++n).padStart(3, "0")}-${label}.png` });
}

test("#1106 Recover this backup set: pre-flight, a finished run, and a cancelled one", async ({ page }) => {
  await page.setViewportSize({ width: 1100, height: 860 });
  await open(page, "/sets/production/postgres-primary", "assurance-set-recover");
  await page.getByRole("button", { name: "Recover this backup set", exact: true }).click();
  const dlg = page.getByRole("dialog", { name: "Recover this backup set" });
  await expect(dlg.getByTestId("preflight-summary")).toContainText("4 backups: 3 to recover");
  await frame(page, "preflight");

  // #1191: the source host is the default now; this pass is about staging.
  await dlg.getByRole("radio", { name: "Folder under the staging root on the host" }).check();
  await dlg.getByRole("textbox", { name: /Name under the staging root/ }).fill("restore-1");
  await expect(dlg.getByText("retnd recover --set production/postgres-primary --to restore-1", { exact: true })).toBeVisible();
  await frame(page, "destination-and-command");
  await dlg.getByRole("radio", { name: "The newest restore point only" }).check();
  await expect(dlg.getByTestId("preflight-summary")).toContainText("1 to recover");
  await frame(page, "newest-only");
  await dlg.getByRole("radio", { name: "Every retained restore point" }).check();
  await expect(dlg.getByTestId("preflight-summary")).toContainText("3 to recover");

  await dlg.getByRole("button", { name: "Recover this backup set" }).click();
  await expect(dlg.getByTestId("running")).toBeVisible();
  for (let i = 0; i < 5; i++) {
    await page.waitForTimeout(450);
    await frame(page, "running");
  }
  await expect(dlg.getByTestId("result")).toContainText("3 of 4 backups recovered", { timeout: 15_000 });
  await expect(dlg.getByTestId("result-manifest")).toContainText("recovery-manifest.json");
  await frame(page, "result");
  await setTheme(page, "dark");
  await expect(dlg.getByTestId("result")).toBeVisible();
  await frame(page, "result-dark");
  await setTheme(page, "light");
  await dlg.getByRole("button", { name: "Close" }).click();

  // The same set again, on the slow host, cancelled after the first file.
  await open(page, "/sets/production/postgres-primary", "assurance-set-slow");
  await page.getByRole("button", { name: "Recover this backup set", exact: true }).click();
  const slow = page.getByRole("dialog", { name: "Recover this backup set" });
  await slow.getByRole("radio", { name: "Folder under the staging root on the host" }).check();
  await slow.getByRole("textbox", { name: /Name under the staging root/ }).fill("restore-2");
  await expect(slow.getByTestId("preflight-summary")).toContainText("3 to recover");
  await slow.getByRole("button", { name: "Recover this backup set" }).click();
  await expect(slow.getByTestId("running-lines").locator("li")).toHaveCount(1, { timeout: 8000 });
  await frame(page, "slow-running");
  await slow.getByRole("button", { name: "Cancel the recovery" }).click();
  await expect(slow.getByTestId("running")).toContainText("Cancelling");
  await frame(page, "cancelling");
  await expect(slow.getByTestId("result")).toHaveAttribute("data-cancelled", "true", { timeout: 8000 });
  await expect(slow.getByTestId("result-cancelled-note")).toContainText("no half-written file was kept");
  await frame(page, "cancelled");
  await page.waitForTimeout(600);
  await frame(page, "cancelled-hold");
});
