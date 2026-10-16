/**
 * Issue #1191, as #1193's review reshaped it: what every recovery dialog says
 * about the "Source host (SFTP)" destination, held once and asserted from each
 * dialog's own spec.
 *
 * The mock host answers the way the engine does. Opening a dialog only
 * DESCRIBES the source host (configuration, no network); the write check that
 * puts a probe file on the host runs when the operator ticks "I understand
 * this writes ..." or presses "Check write access". Scenarios: an SFTP set it
 * can write to by default; a read-only pull account under "source-read-only"
 * (found by the check); a set declared read_only under "source-set-read-only"
 * (never checked); a probe that cannot be removed under
 * "source-probe-left-file"; a local mount under "source-not-sftp".
 */
import { expect } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { RECOVERY_PLACE_LABELS } from "../src/assurance/recoveryPlaces";

export const SOURCE = RECOVERY_PLACE_LABELS.source;
export const STAGING = RECOVERY_PLACE_LABELS.staging;
export const DOWNLOAD = "Download in this browser";
export const ACK = /I understand this writes/;

/** How many times the page asked the engine to describe, and to write-check,
 *  one set's source host (the mock's own counters). */
export async function sourceCalls(page: Page, setId: string): Promise<{ describes: number; writeChecks: number }> {
  return page.evaluate((id) => {
    const p = (window as unknown as { __retndSourceHost?: { describes: Record<string, number>; writeChecks: Record<string, number> } }).__retndSourceHost;
    return { describes: p?.describes[id] ?? 0, writeChecks: p?.writeChecks[id] ?? 0 };
  }, setId);
}

/** The source radio is the preselected choice and names where it writes,
 *  and nothing has been written there yet. */
export async function expectSourcePreselected(dlg: Locator, host: string, folder: string) {
  const radio = dlg.getByRole("radio", { name: SOURCE });
  await expect(radio).toBeVisible();
  await expect(radio).toBeEnabled();
  await expect(radio).toBeChecked();
  const where = dlg.getByTestId("source-destination");
  await expect(where).toContainText("sftp://");
  await expect(where).toContainText(host);
  await expect(where).toContainText(folder + "retnd-recovered-");
  const pre = dlg.getByTestId("source-preflight");
  await expect(pre).toHaveAttribute("data-write-access", "not_checked");
  await expect(pre).toContainText("when you tick the box");
}

/** A writable account: the tick runs the write check, and the act waits for
 *  both. */
export async function expectWritableAndGated(dlg: Locator, go: Locator) {
  await expect(go).toBeDisabled();
  await dlg.getByRole("checkbox", { name: ACK }).check();
  const pre = dlg.getByTestId("source-preflight");
  await expect(pre).toHaveAttribute("data-write-access", "writable");
  // Never into the set's own folder: a new sub-folder is the default.
  await expect(pre).toContainText("new folder");
  await expect(go).toBeEnabled();
}

/** A read-only account, found by the write check: said before the run, and
 *  the run is not offered. */
export async function expectReadOnlyWarning(dlg: Locator, go: Locator) {
  await dlg.getByRole("checkbox", { name: ACK }).check();
  const pre = dlg.getByTestId("source-preflight");
  await expect(pre).toHaveAttribute("data-write-access", "read_only");
  await expect(pre).toHaveAttribute("role", "alert");
  await expect(pre).toContainText("read-only");
  await expect(pre).toContainText("refused");
  await expect(go).toBeDisabled();
}

/** The source host cannot be used here: its radio stays first, switched off,
 *  the reason is said, and the dialog starts where it did before #1191. */
export async function expectFallback(dlg: Locator, fallback: string, reason: string | RegExp) {
  const radio = dlg.getByRole("radio", { name: SOURCE });
  await expect(radio).toBeVisible();
  await expect(radio).toBeDisabled();
  await expect(dlg.getByRole("radio", { name: fallback })).toBeChecked();
  await expect(dlg.getByTestId("source-unavailable")).toContainText(reason);
}
