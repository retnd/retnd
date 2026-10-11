import { expect, test } from "@playwright/test";
import { open } from "./helpers";

const SET = "/sets/production/postgres-primary";

test.describe("#985 reconnect and continuity", () => {
  test("a reload mid-recovery-test restores the stage and bytes and starts no second run", async ({ page }) => {
    await open(page, SET, "assurance-slow-test");
    const section = page.getByRole("region", { name: "Assurance" });
    await section.getByRole("button", { name: "Run recovery test" }).click();
    const prog = section.getByTestId("operation-progress");
    await expect(prog).toHaveAttribute("data-stage", /read|digest/, { timeout: 15_000 });
    const before = await prog.getAttribute("data-bytes");
    expect(Number(before)).toBeGreaterThan(0);

    await page.reload();
    const after = page.getByRole("region", { name: "Assurance" }).getByTestId("operation-progress");
    await expect(after).toBeVisible();
    await expect(after).toHaveAttribute("data-stage", /read|digest/);
    expect(Number(await after.getAttribute("data-bytes"))).toBeGreaterThanOrEqual(Number(before));
    const state = JSON.parse((await page.evaluate(() => sessionStorage.getItem("retnd.mock.assurance"))) ?? "{}");
    expect(state.starts).toBe(1);
  });

  test("a dropped connection replays from its cursor with no duplicate terminal lines", async ({ page }) => {
    await open(page, SET, "assurance-slow-test");
    const section = page.getByRole("region", { name: "Assurance" });
    await section.getByRole("button", { name: "Run recovery test" }).click();
    await expect(section.getByTestId("operation-progress")).toBeVisible();
    await page.evaluate(() => sessionStorage.setItem("retnd.mock.assurance.drop", "1"));
    await expect(section.getByTestId("operation-progress")).toHaveAttribute("data-stage", /digest|validator/, { timeout: 20_000 });
    const lines = await section.getByTestId("operation-line").allTextContents();
    expect(new Set(lines).size).toBe(lines.length);
    const seqs = await section.getByTestId("operation-line").evaluateAll((els) => els.map((e) => e.getAttribute("data-seq")));
    expect(new Set(seqs).size).toBe(seqs.length);
  });

  test("the operation detail view follows the live stage and can cancel", async ({ page }) => {
    await open(page, SET, "assurance-slow-test");
    const section = page.getByRole("region", { name: "Assurance" });
    await section.getByRole("button", { name: "Run recovery test" }).click();
    await section.getByRole("link", { name: "Operation detail" }).click();
    await expect(page.getByRole("checkbox", { name: "Following" })).toBeChecked();
    await page.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByTestId("operation-progress")).toHaveAttribute("data-state", "cancelled");
  });
});

test.describe("#985 validator output", () => {
  test("OSC and window-control sequences are displayed inert and truncation is a marker", async ({ page }) => {
    await open(page, SET, "assurance-validator-output");
    const title = await page.title();
    await page.getByRole("region", { name: "Assurance" }).getByRole("button", { name: "Show validator output" }).click();
    const log = page.getByTestId("validator-log");
    await expect(log).toContainText("\\x1b]0;pwned\\x07");
    await expect(log).toContainText("\\x1b[2J");
    expect(await page.title()).toBe(title);
    const marker = page.getByTestId("truncation-marker");
    await expect(marker).toHaveCount(1);
    await expect(marker).toContainText(/truncated/i);
    // the marker is drawn outside the content stream, and a forged marker line
    // inside the validator's own output is content, not a marker
    expect(await marker.evaluate((m, l) => l!.contains(m), await log.elementHandle())).toBe(false);
    await expect(log.getByText("[output truncated]")).toBeVisible();
    await expect(log.locator('[data-testid="truncation-marker"]')).toHaveCount(0);
  });
});
