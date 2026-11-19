/**
 * Issue #1252: the configure-a-destination wizard's terminal lines.
 *
 * Driven from Settings through "Add a destination", which is the screen the
 * wizard is reached from, against the in-process mock. Every line asserted
 * here is what core/cliecho prints for the same request, so the line beside
 * a control and the one the terminal records after it are the same command.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { contrastReport, open, setTheme } from "./helpers";

test.describe.configure({ timeout: 60_000 });

async function startAdding(page: Page, backend: RegExp, name: string) {
  await open(page, "/settings");
  const card = page.getByRole("region", { name: "Storage destinations" });
  await card.getByRole("button", { name: "Add a destination", exact: true }).click();
  await page.getByRole("radio", { name: backend }).check();
  await page.getByRole("button", { name: "Next: name this instance", exact: true }).click();
  await page.getByLabel("Instance name").fill(name);
  await page.getByRole("button", { name: "Next: confirm", exact: true }).click();
  await expect(page.getByRole("button", { name: "Next: configure it", exact: true })).toBeVisible();
}

function fieldControl(page: Page, fieldId: string) {
  return page.getByTestId(`manifest-field-row-${fieldId}`).getByTestId("manifest-field-control");
}

function wizard(page: Page, name: string) {
  return page.getByRole("group", { name: `Configure ${name}` });
}

test("1252: the add wizard names medium backends and medium configure", async ({ page }) => {
  await startAdding(page, /^Local volume/, "usb_dock");
  const add = page.getByRole("group", { name: "Add a destination" });
  await expect(add).toContainText("retnd medium backends");
  await expect(add).toContainText("retnd medium configure");
  await expect(add).not.toContainText("retnd medium add");
});

test("1252: a local volume's twin is medium configure with --field path, and no gap note", async ({ page }) => {
  await startAdding(page, /^Local volume/, "usb_dock");
  await page.getByRole("button", { name: "Next: configure it", exact: true }).click();

  const w = wizard(page, "usb_dock");
  await fieldControl(page, "path").fill("/mnt/usb-dock");
  await page.getByRole("button", { name: "Next: test connection", exact: true }).click();

  // The test step checks the unsaved values, so its twin writes nothing.
  await expect(page.getByRole("button", { name: "Next: review", exact: true })).toBeEnabled({ timeout: 15_000 });
  await expect(w.locator("code")).toHaveText([
    "retnd medium configure usb_dock --backend local_volume --field path=/mnt/usb-dock --preflight"
  ]);
  await expect(w).not.toContainText("medium test-connection");
  await expect(w).not.toContainText("already saved");

  await page.getByRole("button", { name: "Next: review", exact: true }).click();
  await expect(w.locator("code")).toHaveText([
    "retnd medium configure usb_dock --backend local_volume --field path=/mnt/usb-dock"
  ]);
  await expect(page.getByTestId("configure-command-gap")).toHaveCount(0);
  await expect(w).not.toContainText("cannot carry");
  await expect(w).not.toContainText("retnd medium add");
});

test("1252: making it the default prints medium default", async ({ page }) => {
  await startAdding(page, /^Local volume/, "usb_dock");
  await page.getByRole("button", { name: "Next: configure it", exact: true }).click();

  const w = wizard(page, "usb_dock");
  await fieldControl(page, "path").fill("/mnt/usb-dock");
  await page.getByRole("button", { name: "Next: test connection", exact: true }).click();
  await expect(page.getByRole("button", { name: "Next: review", exact: true })).toBeEnabled({ timeout: 15_000 });
  await page.getByRole("button", { name: "Next: review", exact: true }).click();

  await expect(w).not.toContainText("retnd medium default");
  await w.getByRole("checkbox", { name: /default/i }).check();
  await expect(w.locator("code")).toHaveText([
    "retnd medium configure usb_dock --backend local_volume --field path=/mnt/usb-dock",
    "retnd medium default usb_dock"
  ]);
});

test("1252: an S3 twin carries its fields as --field and the credential as a reference", async ({ page }) => {
  await startAdding(page, /^S3 or S3-compatible/, "cold_archive");
  await page.getByRole("button", { name: "Next: configure it", exact: true }).click();

  const w = wizard(page, "cold_archive");
  await fieldControl(page, "bucket").fill("acme-backups");
  await fieldControl(page, "region").fill("eu-central-1");
  await fieldControl(page, "prefix").fill("annual");
  await fieldControl(page, "credentials").fill("EXAMPLE-NOT-A-REAL-KEY");
  await page.getByRole("textbox", { name: "Access key — secret" }).fill("CANARY-1252-DO-NOT-PRINT");
  await page.getByRole("button", { name: "Next: test connection", exact: true }).click();

  await expect(page.getByRole("button", { name: "Next: review", exact: true })).toBeEnabled({ timeout: 15_000 });
  const fields =
    "retnd medium configure cold_archive --backend s3 --field bucket=acme-backups" +
    " --field prefix=annual --field region=eu-central-1 --credentials-id mock-credential-";
  const test1 = (await w.locator("code").allTextContents()).join("\n");
  expect(test1).toMatch(new RegExp(escape(fields) + "\\d+ --preflight$"));

  await page.getByRole("button", { name: "Next: review", exact: true }).click();
  const review = (await w.locator("code").allTextContents()).join("\n");
  expect(review).toMatch(new RegExp("^" + escape(fields) + "\\d+$"));
  await expect(page.getByTestId("configure-command-gap")).toHaveCount(0);

  const shown = (await w.textContent()) ?? "";
  expect(shown).not.toContain("CANARY-1252-DO-NOT-PRINT");
  expect(shown).not.toContain("EXAMPLE-NOT-A-REAL-KEY");
  expect(shown).not.toContain("retnd medium add");
});

test("1252: an endpoint the cut can't read is held back, with a note that meets AA", async ({ page }) => {
  // The form refuses a username or password in an endpoint, so the case
  // that reaches this screen is a legitimate "@" after the host. The line
  // can't tell that from a password with a "/" in it, so it holds the
  // value back rather than guess (review M1), and says the line won't run.
  await startAdding(page, /^S3 or S3-compatible/, "cold_archive");
  await page.getByRole("button", { name: "Next: configure it", exact: true }).click();

  const w = wizard(page, "cold_archive");
  await fieldControl(page, "bucket").fill("acme-backups");
  await fieldControl(page, "endpoint").fill("https://minio.example.test/tenant@eu");
  await fieldControl(page, "credentials").fill("EXAMPLE-NOT-A-REAL-KEY");
  await page.getByRole("textbox", { name: "Access key — secret" }).fill("CANARY-1252-DO-NOT-PRINT");
  await page.getByRole("button", { name: "Next: test connection", exact: true }).click();
  await expect(page.getByRole("button", { name: "Next: review", exact: true })).toBeEnabled({ timeout: 15_000 });

  for (const step of ["test", "review"]) {
    if (step === "review") await page.getByRole("button", { name: "Next: review", exact: true }).click();
    const line = (await w.locator("code").allTextContents()).join("\n");
    expect(line, step).toContain(
      "--field <endpoint=the value, left out because it looks like it carries credentials>"
    );
    expect(line, step).not.toContain("minio.example.test");
    const note = page.getByTestId("configure-endpoint-note");
    await expect(note, step).toContainText("won't run as printed");
  }

  for (const theme of ["light", "dark"] as const) {
    await setTheme(page, theme);
    const report = await contrastReport(page, "[data-testid=configure-endpoint-note]");
    expect(report.length, theme).toBeGreaterThan(0);
    for (const m of report) {
      console.log(`1252 note contrast, ${theme}: ${m.ratio.toFixed(2)}:1 (needs ${m.need}) ${m.fg} on ${m.bg}`);
      expect(m.ratio, `${theme} ${m.text}`).toBeGreaterThanOrEqual(m.need);
    }
  }
});

function escape(s: string) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
