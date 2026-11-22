import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { open } from "./helpers";
import { openAgents, region } from "./1351-agents";

/**
 * #1646: a tooltip a trigger button opened (by hover or by Tab focus) must not
 * stay up over the dialog that same button opens. Three dialogs, one per kind:
 * the Retire dialog (#1625's last frame), the Create token form, and a
 * ConfirmationDialog (Rotate key). Each is driven twice, with the pointer
 * (hover, then click) and with the keyboard (Tab to the button, Enter).
 */
// A pop-up that is on screen: the closed ones stay mounted with `hidden`. Not
// getByRole("tooltip"), since a pop-up whose entry has no label carries no role.
const tip = (page: Page) => page.locator(".tooltip__pop:not([hidden])");

interface Case {
  name: string;
  go: (page: Page) => Promise<void>;
  trigger: (page: Page) => Locator;
  dialog: (page: Page) => Locator;
}

const cases: Case[] = [
  {
    name: "Retire",
    go: (page) => open(page, "/backups/media/weekly-archive/media-week34.tar", "retire-forge"),
    trigger: (page) => page.getByRole("button", { name: "Retire…" }),
    dialog: (page) => page.getByRole("dialog")
  },
  {
    name: "Create token",
    go: (page) => openAgents(page, "default"),
    trigger: (page) => region(page, "tokens").getByRole("button", { name: "Create token" }),
    dialog: (page) => page.getByRole("dialog", { name: "Create token" })
  },
  {
    name: "Rotate key",
    go: (page) => open(page, "/settings", "encryption-on"),
    trigger: (page) => page.locator("[data-encryption-card]").getByRole("button", { name: "Rotate key" }),
    dialog: (page) => page.getByRole("dialog", { name: "Rotate the managed key?" })
  }
];

async function tabTo(page: Page, el: Locator) {
  for (let i = 0; i < 200; i++) {
    await page.keyboard.press("Tab");
    if (await el.evaluate((n) => n === document.activeElement)) return;
  }
  throw new Error("Tab never reached the trigger");
}

test.describe("#1646 no tooltip over the dialog its trigger opens", () => {
  for (const c of cases) {
    test("#1646 " + c.name + ": hover the trigger, click it, and no tooltip sits over the dialog", async ({ page }) => {
      await c.go(page);
      const trigger = c.trigger(page);
      await trigger.hover();
      await expect(tip(page)).toHaveCount(1);
      await trigger.click();
      await expect(c.dialog(page)).toBeVisible();
      await expect(tip(page)).toHaveCount(0);
      // Still none a moment later, with the pointer resting where it was.
      await page.waitForTimeout(300);
      await expect(tip(page)).toHaveCount(0);
    });

    test("#1646 " + c.name + ": Tab to the trigger, press Enter, and no tooltip sits over the dialog", async ({ page }) => {
      await c.go(page);
      const trigger = c.trigger(page);
      await tabTo(page, trigger);
      await expect(tip(page)).toHaveCount(1);
      await page.keyboard.press("Enter");
      await expect(c.dialog(page)).toBeVisible();
      await expect(tip(page)).toHaveCount(0);
      await page.waitForTimeout(300);
      await expect(tip(page)).toHaveCount(0);
    });
  }

  // Something puts focus back on the control behind an open dialog (the Retire
  // dialog's hand-back timer does, whenever focus has fallen to the body while
  // it is up). The control is covered and nobody asked for its help.
  for (const c of cases) {
    test("#1646 " + c.name + ": focus landing on the covered trigger while the dialog is open opens no tooltip", async ({ page }) => {
      await c.go(page);
      await c.trigger(page).click();
      await expect(c.dialog(page)).toBeVisible();
      // first(): the dialog has a button of the same name (Create token, Rotate key).
      await c.trigger(page).first().evaluate((el) => (el as HTMLElement).focus());
      await page.waitForTimeout(300);
      await expect(tip(page)).toHaveCount(0);
    });
  }
});
