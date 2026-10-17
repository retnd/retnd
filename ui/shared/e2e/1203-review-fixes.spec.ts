/**
 * Issue #1203, the review round: what the script file picker does with a file
 * it should not take whole, and what the remove confirmation tells the operator
 * the matching backups fall back to.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const card = (p: Page) => p.getByRole("region", { name: "Recovery-test validators" });

async function openAdd(page: Page) {
  await open(page, "/settings");
  await card(page).getByRole("button", { name: "Add validator" }).click();
  const dlg = page.getByRole("dialog", { name: "Add validator" });
  await expect(dlg).toBeVisible();
  return dlg;
}

test.describe("#1203 review: the file picker", () => {
  test("a file that is not UTF-8 is refused, not loaded with replacement characters", async ({ page }) => {
    const dlg = await openAdd(page);
    await dlg.getByLabel("Script file").setInputFiles({ name: "bin.sh", mimeType: "text/plain", buffer: Buffer.from([0x23, 0x21, 0xff, 0xfe, 0x0a]) });
    await expect(dlg.locator("[data-validator-file-problem]")).toContainText("not valid UTF-8");
    await expect(dlg.getByLabel("Script", { exact: true })).toHaveValue("");
  });

  test("a file with Windows line endings is loaded and the dialog says it changed them", async ({ page }) => {
    const dlg = await openAdd(page);
    await dlg.getByLabel("Script file").setInputFiles({ name: "win.sh", mimeType: "text/plain", buffer: Buffer.from("#!/bin/sh\r\nexit 0\r\n") });
    await expect(dlg.getByLabel("Script", { exact: true })).toHaveValue("#!/bin/sh\nexit 0\n");
    await expect(dlg.locator("[data-validator-file-note]")).toContainText("Line endings were converted to LF");
    await dlg.getByLabel("Script", { exact: true }).fill("#!/bin/sh\n");
    await expect(dlg.locator("[data-validator-file-note]")).toHaveCount(0);
  });
});

test.describe("#1203 review: removing a validator", () => {
  test("says how many backups lose it and what they fall back to", async ({ page }) => {
    await open(page, "/settings");
    await card(page).getByRole("button", { name: "Remove billing-dump" }).click();
    const dlg = page.getByRole("dialog", { name: "Remove validator billing-dump" });
    await expect(dlg).toContainText("2 backups match its pattern now");
    await expect(dlg).toContainText("validator_id");
    await expect(dlg).toContainText("recovery-test folder");
    await expect(dlg).toContainText("built-in check");
    await expect(dlg).not.toContainText("deletes its script");
  });
});
