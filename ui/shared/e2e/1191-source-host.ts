/**
 * Issue #1191: what every recovery dialog says about the "Source host (SFTP)"
 * destination, held once and asserted from each dialog's own spec.
 *
 * The mock host answers the preflight for the fixture sets the way the engine
 * does: an SFTP set it can write to by default, a read-only pull account under
 * "source-read-only", and a set whose source is a local mount under
 * "source-not-sftp".
 */
import { expect } from "@playwright/test";
import type { Locator } from "@playwright/test";

export const SOURCE = "Source host (SFTP)";

/** The source radio is the preselected choice and names where it writes. */
export async function expectSourcePreselected(dlg: Locator, host: string, folder: string) {
  const radio = dlg.getByRole("radio", { name: SOURCE });
  await expect(radio).toBeVisible();
  await expect(radio).toBeEnabled();
  await expect(radio).toBeChecked();
  const where = dlg.getByTestId("source-destination");
  await expect(where).toContainText("sftp://");
  await expect(where).toContainText(host);
  await expect(where).toContainText(folder + "retnd-recovered-");
  // Never into the set's own folder: a new sub-folder is the default.
  await expect(dlg.getByTestId("source-preflight")).toContainText("new folder");
}

/** A writable account: the preflight passes, and the act still waits for the tick. */
export async function expectWritableAndGated(dlg: Locator, go: Locator) {
  await expect(dlg.getByTestId("source-preflight")).toHaveAttribute("data-writable", "yes");
  await expect(go).toBeDisabled();
  await dlg.getByRole("checkbox", { name: /I understand this writes onto/ }).check();
  await expect(go).toBeEnabled();
}

/** A read-only account: said before the run, and the run is not offered. */
export async function expectReadOnlyWarning(dlg: Locator, go: Locator) {
  const pre = dlg.getByTestId("source-preflight");
  await expect(pre).toHaveAttribute("data-writable", "no");
  await expect(pre).toHaveAttribute("role", "alert");
  await expect(pre).toContainText("read-only");
  await expect(pre).toContainText("refused");
  const tick = dlg.getByRole("checkbox", { name: /I understand this writes onto/ });
  if (await tick.count()) await tick.check();
  await expect(go).toBeDisabled();
}

/** A non-SFTP source: nothing preselected, and the dialog says why. */
export async function expectNothingPreselected(dlg: Locator, group: string) {
  const radio = dlg.getByRole("radio", { name: SOURCE });
  await expect(radio).toBeVisible();
  await expect(radio).toBeDisabled();
  await expect(dlg.getByRole("radiogroup", { name: group }).getByRole("radio", { checked: true })).toHaveCount(0);
  await expect(dlg.getByTestId("source-unavailable")).toContainText("not SFTP");
}
