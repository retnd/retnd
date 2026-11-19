import { expect, test } from "@playwright/test";
import { open } from "./helpers";

/**
 * Issue #1216: the mock API is loaded with a dynamic import() before the first
 * render. Nothing about booting may change: the scenario picker, `wire=1` and
 * a plain load all still come up on the same screens.
 */
test.describe("#1216 boot with the lazily loaded mock", () => {
  test("a plain load renders the app", async ({ page }) => {
    await open(page, "/");
    await expect(page.getByRole("navigation").first()).toBeVisible();
  });

  test("the scenario query still selects the fixture set", async ({ page }) => {
    await open(page, "/sets/production/postgres-primary", "assurance-no-validator", { wire: true });
    await expect(page.getByRole("region", { name: "Assurance" }).getByText(/no recovery-test validator is declared/i)).toBeVisible();
  });

  test("wire=1 still routes assurance through the production seam", async ({ page }) => {
    await open(page, "/sets/production/postgres-primary", "default", { wire: true });
    await expect(page.getByRole("region", { name: "Assurance" }).getByRole("button", { name: "Run recovery test" })).toBeEnabled();
  });

  test("the live probe hook is still installed on window", async ({ page }) => {
    await open(page, "/");
    await expect(page.getByRole("navigation").first()).toBeVisible();
    expect(await page.evaluate(() => typeof (window as unknown as { __retndLive?: unknown }).__retndLive)).toBe("object");
  });
});
