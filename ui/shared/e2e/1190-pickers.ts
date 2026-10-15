/**
 * Issue #1190: the checks every screen that offers "Folder under the staging
 * root on the host" and "Configured destination" has to pass, written once so
 * each screen's spec holds the same behaviour. A screen that grows its own
 * text field, its own labels or its own list fails here.
 *
 * The shared pieces are the folder field (a name under the staging root, with
 * the host folder browser under it) and the destination drop-down (the
 * destinations Settings lists, or "None configured").
 */
import { expect } from "@playwright/test";
import type { Locator } from "@playwright/test";

export const STAGING = "Folder under the staging root on the host";
export const DESTINATION = "Configured destination";
export const STAGING_ROOT = "/var/lib/retnd/recovery-staging";
/** The destinations the default fixtures declare, as Settings lists them. */
export const SETTINGS_DESTINATIONS = ["local", "usb_shelf", "offsite_s3", "offsite_cold"];

export const folderField = (scope: Locator) => scope.getByTestId("staging-folder-field");
export const destinationField = (scope: Locator) => scope.getByTestId("destination-field");
export const nameBox = (scope: Locator) => folderField(scope).getByRole("textbox", { name: "Name under the staging root", exact: true });
export const destinationBox = (scope: Locator) => destinationField(scope).getByRole("combobox", { name: DESTINATION, exact: true });
const browserOf = (scope: Locator) => folderField(scope).getByRole("group", { name: "Folders on the host" });

/** Both choices are offered under the same words. */
export async function offersBothChoices(scope: Locator) {
  await expect(scope.getByRole("radio", { name: STAGING, exact: true })).toHaveCount(1);
  await expect(scope.getByRole("radio", { name: DESTINATION, exact: true })).toHaveCount(1);
}

/** The staging choice is the shared folder field: the root named, a text
 *  field under one name, and the host's folders to browse into it. */
export async function stagingIsSharedFolderField(scope: Locator, expected: string) {
  await scope.getByRole("radio", { name: STAGING, exact: true }).check();
  const field = folderField(scope);
  await expect(field).toBeVisible();
  await expect(field).toContainText(STAGING_ROOT);
  await expect(nameBox(scope)).toBeVisible();
  const browser = browserOf(scope);
  await expect(browser).toBeVisible();
  await browser.getByRole("button", { name: "Open folder daily" }).click();
  await browser.getByRole("button", { name: "Open folder 2026-10" }).click();
  await expect(browser.getByTestId("browser-path")).toHaveText("daily/2026-10");
  await browser.getByRole("button", { name: "Use this folder" }).click();
  await expect(nameBox(scope)).toHaveValue(expected);
}

/** The destination choice is the shared drop-down of Settings' destinations,
 *  and no screen asks for a destination id in a text box. */
export async function destinationIsSharedDropDown(scope: Locator, command: Locator) {
  await scope.getByRole("radio", { name: DESTINATION, exact: true }).check();
  await expect(destinationField(scope)).toBeVisible();
  const select = destinationBox(scope);
  await expect(select.locator("option")).toHaveCount(SETTINGS_DESTINATIONS.length + 1);
  const values = await select.locator("option").evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value));
  expect(values).toEqual(["", ...SETTINGS_DESTINATIONS]);
  await expect(scope.getByRole("textbox", { name: /destination/i })).toHaveCount(0);
  await select.selectOption("offsite_s3");
  await expect(command).toContainText("medium:offsite_s3");
}

/** With no destination in Settings the drop-down says so and the action waits. */
export async function noneConfigured(scope: Locator, action: Locator) {
  await scope.getByRole("radio", { name: DESTINATION, exact: true }).check();
  const select = destinationBox(scope);
  await expect(select).toBeVisible();
  await expect(select).toBeDisabled();
  await expect(select).toContainText("None configured");
  await expect(action).toBeDisabled();
}

/** A host that cannot list its folders says so, and the field still takes a
 *  typed name, which reaches the terminal command. */
export async function browseUnavailable(scope: Locator, command: Locator) {
  await scope.getByRole("radio", { name: STAGING, exact: true }).check();
  await expect(browserOf(scope).getByRole("alert")).toContainText("cannot list its folders");
  await nameBox(scope).fill("typed-by-hand");
  await expect(command).toContainText("--to typed-by-hand");
}
