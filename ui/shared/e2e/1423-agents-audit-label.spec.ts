/**
 * Issue #1423: the release browser lane's "every control is reachable and
 * labelled" sweep (retnd-tests, accessibility.spec.ts) reports the Audit
 * "Since" input in the Settings Agents card. The sweep accepts a control that
 * has an aria-label, a title, text of its own or a wrapping label, so this
 * repeats exactly that rule over the card and adds the accessible-name check.
 *
 * Playwright only through /Users/rom/workspace/xdg-pw-lock.sh, one run at a
 * time.
 */
import { expect, test } from "@playwright/test";
import { card, openAgents, region } from "./1351-agents";

test.describe("#1423 the Agents audit filters are labelled", () => {
  test("#1423 no control in the Agents card is unlabelled by the lane's sweep", async ({ page }) => {
    await openAgents(page);
    const unlabelled = await card(page).evaluate((root) =>
      Array.from(root.querySelectorAll("button, a[href], input, select, textarea"))
        .filter((el) => {
          const style = getComputedStyle(el);
          if (style.display === "none" || style.visibility === "hidden") return false;
          const label =
            el.getAttribute("aria-label") ?? el.getAttribute("title") ?? (el as HTMLElement).innerText ?? "";
          if (label.trim().length > 0) return false;
          return !el.closest("label");
        })
        .map((el) => el.tagName + (el.className ? "." + String(el.className).slice(0, 40) : ""))
    );
    expect(unlabelled, "unlabelled controls in the Agents card").toEqual([]);
  });

  test("#1423 the Since input is named Since, and the Token select keeps its name", async ({ page }) => {
    await openAgents(page);
    await expect(region(page, "audit").getByRole("textbox", { name: "Since" })).toHaveAttribute("aria-label", "Since");
    await expect(region(page, "audit").getByRole("combobox", { name: "Token" })).toBeVisible();
  });
});
