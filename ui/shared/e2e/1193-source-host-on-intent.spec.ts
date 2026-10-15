/**
 * #1193's review of the "Source host (SFTP)" destination (#1191), on the real
 * screens: the write check that puts a probe file on a production host runs
 * only when the operator asks for it, the default applies only where it can
 * work, the acknowledgement names the bytes, a set run onto the source starts
 * on the newest restore point, and a refused password goes back to the field
 * that asked for it.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, row, setTheme } from "./helpers";
import { ACK, DOWNLOAD, SOURCE, STAGING, expectFallback, sourceCalls } from "./1191-source-host";

const SET = "production/postgres-primary";
const PG = "postgres-prod-20260828.dump.zst";
const artifactDialog = (p: Page) => p.getByRole("dialog", { name: "Recover artifact" });
const setDialog = (p: Page) => p.getByRole("dialog", { name: "Recover this backup set" });
const bar = (p: Page) => p.getByRole("region", { name: "Selected backups" });
const rowBox = (p: Page, f: string) => p.getByRole("checkbox", { name: "Select " + f, exact: true });
/** "1.2 GB", "915 MB", "12 KB"... whatever bytes() printed. */
const AMOUNT = /\d[\d.,]* (B|KB|MB|GB|TB|KiB|MiB|GiB|TiB)/;

async function listDialog(page: Page, scenario = "default") {
  await open(page, "/backups", scenario);
  await row(page, PG).click();
  await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
  await expect(artifactDialog(page)).toBeVisible();
  return artifactDialog(page);
}
async function pageDialog(page: Page, scenario = "default") {
  await open(page, "/backups/" + SET + "/" + PG, scenario);
  await page.getByRole("region", { name: "Assurance" }).getByRole("button", { name: "Recover artifact" }).click();
  await expect(artifactDialog(page)).toBeVisible();
  return artifactDialog(page);
}
async function theSetDialog(page: Page, scenario = "assurance-set-recover") {
  await open(page, "/sets/" + SET, scenario);
  await page.getByRole("button", { name: "Recover this backup set", exact: true }).click();
  await expect(setDialog(page)).toBeVisible();
  return setDialog(page);
}

test.describe("#1193 nothing is written on the source host because a dialog opened", () => {
  test("opening each of the three dialogs describes the host and never write-checks it", async ({ page }) => {
    let dlg = await listDialog(page);
    await expect(dlg.getByTestId("source-destination")).toContainText("sftp://");
    await page.waitForTimeout(700);
    expect((await sourceCalls(page, SET)).writeChecks).toBe(0);
    expect((await sourceCalls(page, SET)).describes).toBeGreaterThan(0);

    dlg = await pageDialog(page);
    await expect(dlg.getByTestId("source-destination")).toContainText("sftp://");
    await page.waitForTimeout(700);
    expect((await sourceCalls(page, SET)).writeChecks).toBe(0);

    dlg = await theSetDialog(page);
    await expect(dlg.getByTestId("source-destination")).toContainText("sftp://");
    await page.waitForTimeout(700);
    expect((await sourceCalls(page, SET)).writeChecks).toBe(0);
  });

  test("moving the Backups selection between sets describes each set and checks none", async ({ page }) => {
    await open(page, "/backups", "backups-many");
    await rowBox(page, "postgres-primary-20260801.bak").check();
    await expect(bar(page).getByTestId("source-destination")).toContainText("prod-db-01.internal");
    await rowBox(page, "postgres-primary-20260801.bak").uncheck();
    await rowBox(page, "billing-mysql-20260802.bak").check();
    await expect(bar(page).getByTestId("source-destination")).toContainText("billing-db.internal");
    await page.waitForTimeout(700);
    const billing = (await page.evaluate(() => (window as unknown as { __retndSourceHost: { writeChecks: Record<string, number> } }).__retndSourceHost.writeChecks));
    expect(Object.values(billing).reduce((a, b) => a + b, 0)).toBe(0);
  });
});

test.describe("#1193 the write check runs on the operator's say-so", () => {
  test("ticking the acknowledgement checks once, and ticking again reuses the answer", async ({ page }) => {
    const dlg = await listDialog(page);
    const tick = dlg.getByRole("checkbox", { name: ACK });
    await tick.check();
    await expect(dlg.getByTestId("source-preflight")).toHaveAttribute("data-write-access", "writable");
    expect((await sourceCalls(page, SET)).writeChecks).toBe(1);
    await tick.uncheck();
    await tick.check();
    await expect(dlg.getByRole("button", { name: "Recover artifact" })).toBeEnabled();
    expect((await sourceCalls(page, SET)).writeChecks).toBe(1);
  });

  test("Check write access runs it without ticking, and says how much space is free", async ({ page }) => {
    const dlg = await listDialog(page);
    await dlg.getByRole("button", { name: "Check write access" }).click();
    await expect(dlg.getByTestId("source-preflight")).toHaveAttribute("data-write-access", "writable");
    expect((await sourceCalls(page, SET)).writeChecks).toBe(1);
    await expect(dlg.getByTestId("source-space")).toContainText(/prod-db-01\.internal has \d[\d.]* TB free/);
    // Still waits for the tick.
    await expect(dlg.getByRole("button", { name: "Recover artifact" })).toBeDisabled();
  });

  test("a host that cannot say how much is free is said in words", async ({ page }) => {
    const dlg = await listDialog(page, "source-space-unknown");
    await dlg.getByRole("checkbox", { name: ACK }).check();
    await expect(dlg.getByTestId("source-space")).toContainText("did not say how much space is free");
  });

  test("a host that did not answer keeps Recover off and offers the check again", async ({ page }) => {
    const dlg = await listDialog(page, "source-unreachable");
    await dlg.getByRole("checkbox", { name: ACK }).check();
    const pre = dlg.getByTestId("source-preflight");
    await expect(pre).toHaveAttribute("data-write-access", "unreachable");
    await expect(pre).toHaveAttribute("role", "alert");
    await expect(dlg.getByRole("button", { name: "Check write access" })).toBeEnabled();
    await expect(dlg.getByRole("button", { name: "Recover artifact" })).toBeDisabled();
  });

  test("a probe file the host would not let go is named, and the host is not treated as writable", async ({ page }) => {
    let dlg = await listDialog(page, "source-probe-left-file");
    await dlg.getByRole("checkbox", { name: ACK }).check();
    const pre = dlg.getByTestId("source-preflight");
    await expect(pre).toHaveAttribute("data-write-access", "probe_left_file");
    await expect(pre).toContainText("Delete /backups/postgresql/.retnd-write-probe-5f2a91c4 on prod-db-01.internal");
    await expect(dlg.getByRole("button", { name: "Recover artifact" })).toBeDisabled();
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      expect(await measureContrast(page, "1193 probe left " + theme, "[data-testid=source-preflight]"), theme).toEqual([]);
    }
    // Opened again: the answer is remembered, so nothing is preselected and
    // nothing is probed again.
    await dlg.getByRole("button", { name: "Cancel" }).click();
    await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
    dlg = artifactDialog(page);
    await expectFallback(dlg, DOWNLOAD, ".retnd-write-probe-5f2a91c4");
    expect((await sourceCalls(page, SET)).writeChecks).toBe(1);
  });

  test("a shut gate never checks, and says why", async ({ page }) => {
    const dlg = await listDialog(page, "gateway-profile");
    await expectFallback(dlg, DOWNLOAD, "will not write onto the source host");
    await page.waitForTimeout(700);
    expect((await sourceCalls(page, SET)).writeChecks).toBe(0);
  });
});

test.describe("#1193 the default applies only where it can work", () => {
  test("a set declared read_only is not preselected in any dialog, and is never checked", async ({ page }) => {
    let dlg = await listDialog(page, "source-set-read-only");
    await expectFallback(dlg, DOWNLOAD, "read_only");
    await expect(dlg.getByRole("radiogroup", { name: "Where should the artifact go?" }).getByRole("radio").first()).toHaveAccessibleName(SOURCE);

    dlg = await theSetDialog(page, "source-set-read-only");
    await expectFallback(dlg, STAGING, "read_only");
    await expect(dlg.getByRole("radio", { name: "Every retained restore point" })).toBeChecked();

    await open(page, "/backups", "source-set-read-only");
    await rowBox(page, PG).check();
    await expectFallback(bar(page), STAGING, "read_only");
    expect((await sourceCalls(page, SET)).writeChecks).toBe(0);
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      expect(await measureContrast(page, "1193 read-only set " + theme, "[data-testid=source-unavailable]"), theme).toEqual([]);
    }
  });
});

test.describe("#1193 the acknowledgement names what is written", () => {
  test("one backup: its size", async ({ page }) => {
    const dlg = await listDialog(page);
    const label = dlg.getByText(ACK);
    await expect(label).toContainText(AMOUNT);
    await expect(label).toContainText(/onto prod-db-01\.internal, into sftp:\/\/backup-agent@prod-db-01\.internal\/backups\/postgresql\/retnd-recovered-/);
  });

  test("a selection: its total, as the bar counts it", async ({ page }) => {
    await open(page, "/backups", "backups-many");
    await rowBox(page, "postgres-primary-20260801.bak").check();
    const total = ((await bar(page).getByTestId("selection-count").textContent()) ?? "").split("· ").pop()!.trim();
    await expect(bar(page).getByText(ACK)).toContainText("writes " + total + " onto");
  });

  test("a set: the pre-flight's bytes, and newest only by default", async ({ page }) => {
    const dlg = await theSetDialog(page);
    await expect(dlg.getByRole("radio", { name: SOURCE })).toBeChecked();
    await expect(dlg.getByRole("radio", { name: "The newest restore point only" })).toBeChecked();
    await expect(dlg.getByTestId("preflight-summary")).toContainText("1 to recover");
    const summary = (await dlg.getByTestId("preflight-summary").textContent()) ?? "";
    const amount = /to recover \(([^)]+)\)/.exec(summary)?.[1] ?? "?";
    await expect(dlg.getByText(ACK)).toContainText("writes " + amount + " onto");
    // Every restore point is still there to ask for, and the terminal line
    // then says so in so many words.
    await dlg.getByRole("radio", { name: "Every retained restore point" }).check();
    await expect(dlg.getByText(/retnd recover --set production\/postgres-primary --all --to source:retnd-recovered-\S+ --preflight/)).toBeVisible();
    // Off the source host, the old default is back.
    await dlg.getByRole("radio", { name: STAGING }).check();
    await expect(dlg.getByRole("radio", { name: "Every retained restore point" })).toBeChecked();
  });

  test("a set with no operator choice switches between newest and all with the target", async ({ page }) => {
    const dlg = await theSetDialog(page);
    await expect(dlg.getByRole("radio", { name: "The newest restore point only" })).toBeChecked();
    await dlg.getByRole("radio", { name: STAGING }).check();
    await expect(dlg.getByRole("radio", { name: "Every retained restore point" })).toBeChecked();
    await dlg.getByRole("radio", { name: SOURCE }).check();
    await expect(dlg.getByRole("radio", { name: "The newest restore point only" })).toBeChecked();
  });
});

test.describe("#1193 the password goes once and its refusal comes back to the field", () => {
  test("the selection bar shows a wrong password in its own field, with the password cleared", async ({ page }) => {
    await open(page, "/backups", "local-account-profile");
    await rowBox(page, PG).check();
    const b = bar(page);
    await expect(b.getByRole("radio", { name: SOURCE })).toBeChecked();
    await b.getByRole("checkbox", { name: ACK }).check();
    await expect(b.getByTestId("source-preflight")).toHaveAttribute("data-write-access", "writable");
    await b.getByLabel("Your password").fill("not-the-password");
    await b.getByRole("button", { name: "Recover selected" }).click();
    const alert = b.locator("[data-source-step-up-error]");
    await expect(alert).toContainText("That password was not accepted");
    await expect(alert).toContainText("Nothing was written to the source host");
    await expect(page.getByRole("dialog", { name: "Recovering backups" })).toHaveCount(0);
    await expect(b.getByLabel("Your password")).toHaveValue("");
    await expect(b.getByRole("button", { name: "Recover selected" })).toBeDisabled();
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      expect(await measureContrast(page, "1193 bar step-up " + theme, "[data-source-step-up-error]"), theme).toEqual([]);
    }
  });

  test("the right password starts the run onto the source host", async ({ page }) => {
    await open(page, "/backups", "local-account-profile");
    await rowBox(page, PG).check();
    const b = bar(page);
    await b.getByRole("checkbox", { name: ACK }).check();
    await b.getByLabel("Your password").fill("correct-horse-battery");
    await b.getByRole("button", { name: "Recover selected" }).click();
    const run = page.getByRole("dialog", { name: "Recovering backups" });
    await expect(run).toBeVisible();
    await expect(run).toContainText("Source host, new folder retnd-recovered-");
  });

  test("a recovery onto the source says how the placed file was checked", async ({ page }) => {
    const dlg = await listDialog(page);
    await dlg.getByRole("checkbox", { name: ACK }).check();
    await dlg.getByRole("button", { name: "Recover artifact" }).click();
    const placed = dlg.getByTestId("placed-verified");
    await expect(placed).toHaveAttribute("data-verified", "sha256");
    await expect(placed).toContainText("checked by SHA-256 on the host");
  });
});

test.describe("#1193 the new source-host text meets AA", () => {
  for (const theme of ["light", "dark"] as const) {
    test(`the checked field (status, free space, tick, password help) in ${theme}`, async ({ page }) => {
      const dlg = await listDialog(page, "local-account-profile");
      await dlg.getByRole("checkbox", { name: ACK }).check();
      await expect(dlg.getByTestId("source-space")).toBeVisible();
      await setTheme(page, theme);
      expect(await measureContrast(page, "1193 checked field " + theme, "[data-source-host]")).toEqual([]);
    });
    test(`the unchecked field and its Check write access button in ${theme}`, async ({ page }) => {
      const dlg = await listDialog(page, "source-unreachable");
      await expect(dlg.getByRole("button", { name: "Check write access" })).toBeVisible();
      await setTheme(page, theme);
      expect(await measureContrast(page, "1193 unchecked field " + theme, "[data-source-host]")).toEqual([]);
      await dlg.getByRole("checkbox", { name: ACK }).check();
      await expect(dlg.getByTestId("source-preflight")).toHaveAttribute("data-write-access", "unreachable");
      expect(await measureContrast(page, "1193 unreachable " + theme, "[data-source-host]")).toEqual([]);
    });
  }
});
