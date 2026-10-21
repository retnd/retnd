/**
 * Slow pass through adding a local volume, for the GIFs in #1252 and in its
 * PR: the confirm step's sentence about the terminal, the test step's line
 * and the review step's line, each outlined when it is on screen, then the
 * same for an S3 bucket's review step. It asserts nothing about the lines
 * themselves, so the one spec records the broken screens on main and the
 * fixed ones on the branch. The outlined lines are wrapped rather than
 * scrolled, for the recording only. Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);
test.describe.configure({ timeout: 150_000 });
// A recording and nothing else: it asserts nothing about the lines and
// spends most of its time waiting, so the ordinary run leaves it out.
test.skip(!process.env.E2E_VIDEO, "recording only, run with E2E_VIDEO=1");

/** Outlines the printed lines (and the gap note, where there is one) and
 *  scrolls them into view. Test-only; nothing here ships. */
async function mark(page: Page) {
  // Off the buttons, so a tooltip from the last click does not cover a line.
  await page.mouse.move(1180, 120);
  await page.evaluate(() => {
    const targets = document.querySelectorAll(
      "[role=group][aria-label^='Configure '] code, [data-testid=configure-command-gap]," +
        " [role=group][aria-label='Add a destination'] span:has(> code)"
    );
    targets.forEach((e) => {
      const h = e as HTMLElement;
      h.style.outline = "2px dashed #d11a2a";
      h.style.outlineOffset = "2px";
      // The echo scrolls a long line sideways; wrapped here so the whole
      // line is in the recording.
      if (h.tagName === "CODE") h.style.whiteSpace = "pre-wrap";
    });
    targets[targets.length - 1]?.scrollIntoView({ block: "center" });
  });
}

async function slowType(page: Page, fieldId: string, text: string) {
  const control = page.getByTestId(`manifest-field-row-${fieldId}`).getByTestId("manifest-field-control");
  await control.click();
  await control.pressSequentially(text, { delay: 45 });
}

async function add(page: Page, backend: RegExp, name: string) {
  const card = page.getByRole("region", { name: "Storage destinations" });
  await card.scrollIntoViewIfNeeded();
  await beat(page);
  await card.getByRole("button", { name: "Add a destination", exact: true }).click();
  await beat(page);
  await page.getByRole("radio", { name: backend }).check();
  await beat(page, 600);
  await page.getByRole("button", { name: "Next: name this instance", exact: true }).click();
  await page.getByLabel("Instance name").pressSequentially(name, { delay: 45 });
  await page.getByRole("button", { name: "Next: confirm", exact: true }).click();
  await expect(page.getByRole("button", { name: "Next: configure it", exact: true })).toBeVisible();
  await mark(page);
  await beat(page, 2600);
  await page.getByRole("button", { name: "Next: configure it", exact: true }).click();
  await beat(page, 600);
}

async function testAndReview(page: Page) {
  await page.getByRole("button", { name: "Next: test connection", exact: true }).click();
  await expect(page.getByRole("button", { name: "Next: review", exact: true })).toBeEnabled({ timeout: 15_000 });
  await mark(page);
  await beat(page, 2800);
  await page.getByRole("button", { name: "Next: review", exact: true }).click();
  await beat(page, 400);
  await mark(page);
  await beat(page, 2800);
}

test("1252 walkthrough: the configure wizard's terminal lines", async ({ page }) => {
  await open(page, "/settings");
  await expect(page.getByRole("region", { name: "Storage destinations" })).toBeVisible({ timeout: 30_000 });
  await beat(page, 1200);

  await add(page, /^Local volume/, "usb_dock");
  await slowType(page, "path", "/mnt/usb-dock");
  await beat(page, 500);
  await testAndReview(page);
  const box = page.getByRole("group", { name: "Configure usb_dock" }).getByRole("checkbox", { name: /default/i });
  await box.check();
  await beat(page, 300);
  await mark(page);
  await beat(page, 2600);
  await page.getByRole("button", { name: "Back", exact: true }).click();
  await page.getByRole("button", { name: "Back", exact: true }).click();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await beat(page, 600);

  await add(page, /^S3 or S3-compatible/, "cold_archive");
  await slowType(page, "bucket", "acme-backups");
  await slowType(page, "region", "eu-central-1");
  await slowType(page, "credentials", "EXAMPLE-NOT-A-REAL-KEY");
  await page.getByRole("textbox", { name: "Access key — secret" }).pressSequentially("not-a-secret", { delay: 30 });
  await beat(page, 400);
  await testAndReview(page);
});
